// Package api HTTP 层: 路由、认证、统一响应包裹。
package api

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"derp-orch/internal/auth"
	"derp-orch/internal/captcha"
	"derp-orch/internal/config"
	"derp-orch/internal/docker"
	"derp-orch/internal/lifecycle"
	"derp-orch/internal/limits"
	"derp-orch/internal/store"
	"derp-orch/internal/verify"
)

// authSessionCookie admin 会话 cookie 名。
const authSessionCookie = "derp_admin_session"

// Server API 服务。组合全部依赖。
type Server struct {
	cfg     *config.Config
	store   *store.Store
	orch    docker.Orchestrator
	sweep   *lifecycle.Sweeper
	verify  *verify.Handler
	captcha *captcha.Store
	authmgr *auth.Manager
	limits  *limits.Checker

	// 兑换接口 IP 限速(内存)。
	redeemMu   sync.Mutex
	redeemHits map[string][]time.Time
}

// New 构建路由。
func New(cfg *config.Config, st *store.Store, orch docker.Orchestrator, sweep *lifecycle.Sweeper,
	captchaStore *captcha.Store, authmgr *auth.Manager, checker *limits.Checker) http.Handler {
	s := &Server{
		cfg:        cfg,
		store:      st,
		orch:       orch,
		sweep:      sweep,
		verify:     verify.New(st),
		captcha:    captchaStore,
		authmgr:    authmgr,
		limits:     checker,
		redeemHits: map[string][]time.Time{},
	}
	mux := http.NewServeMux()

	// 双入口页面。
	mux.HandleFunc("GET /redeem", s.handleRedeemPage)
	mux.HandleFunc("GET /admin", s.handleAdminPage)
	// 旧入口 / 重定向到兑换面板(不泄露 admin 入口)。
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redeem", http.StatusFound)
	})

	// 公开端点。
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("POST /verify/{instanceID}", s.verify)
	mux.HandleFunc("GET /captcha", s.handleCaptcha)

	// 兑换端点(公开,IP 限速)。
	mux.HandleFunc("POST /v1/redeem/redeem", s.rateLimit(s.handleRedeem))

	// 管理端点(session 认证)。
	mux.HandleFunc("POST /v1/admin/login", s.handleAdminLogin)
	mux.HandleFunc("GET /v1/admin/login/state", s.handleAdminLoginState)
	mux.Handle("POST /v1/admin/password", s.adminAuth(http.HandlerFunc(s.handleAdminChangePassword)))
	mux.Handle("POST /v1/admin/logout", s.adminAuth(http.HandlerFunc(s.handleAdminLogout)))
	mux.Handle("POST /v1/admin/codes", s.adminAuth(http.HandlerFunc(s.handleCreateCodes)))
	mux.Handle("GET /v1/admin/codes", s.adminAuth(http.HandlerFunc(s.handleListCodes)))
	mux.Handle("GET /v1/admin/codes/export", s.adminAuth(http.HandlerFunc(s.handleExportCodes)))
	mux.Handle("GET /v1/admin/stats", s.adminAuth(http.HandlerFunc(s.handleAdminStats)))
	mux.Handle("GET /v1/admin/instances", s.adminAuth(http.HandlerFunc(s.handleAdminInstances)))
	mux.Handle("DELETE /v1/admin/instances/{id}", s.adminAuth(http.HandlerFunc(s.handleAdminDeleteInstance)))
	mux.Handle("GET /v1/admin/instances/{id}/derpmap", s.adminAuth(http.HandlerFunc(s.handleAdminDerpMap)))
	mux.Handle("GET /v1/admin/audit", s.adminAuth(http.HandlerFunc(s.handleAdminAudit)))

	// 旧 Bearer 管理端点,保留向后兼容(内部/脚本用)。
	mux.Handle("POST /v1/instances", s.auth(http.HandlerFunc(s.handleCreateInstance)))
	mux.Handle("GET /v1/instances", s.auth(http.HandlerFunc(s.handleListInstances)))
	mux.Handle("GET /v1/instances/{id}", s.auth(http.HandlerFunc(s.handleGetInstance)))
	mux.Handle("DELETE /v1/instances/{id}", s.auth(http.HandlerFunc(s.handleDeleteInstance)))

	return mux
}

// adminAuth session cookie 认证中间件。
func (s *Server) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.authmgr.Authenticate(r); !ok {
			log.Printf("[api] 未授权访问 %s 来自 %s", r.URL.Path, auth.ClientIP(r))
			writeError(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth Bearer API key 中间件,常数时间比较。
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
			token = h[7:]
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AdminAPIKey)) != 1 {
			log.Printf("[api] 未授权访问 %s 来自 %s", r.URL.Path, r.RemoteAddr)
			writeError(w, http.StatusUnauthorized, "未授权")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimit 兑换接口 IP 限速。
func (s *Server) rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := auth.ClientIP(r)
		limit := s.cfg.RedeemIPRateLimit
		if limit <= 0 {
			limit = 5
		}
		window := time.Duration(s.cfg.RedeemIPWindowSeconds) * time.Second
		if window <= 0 {
			window = 60 * time.Second
		}
		s.redeemMu.Lock()
		now := time.Now()
		cutoff := now.Add(-window)
		hits := s.redeemHits[ip]
		kept := hits[:0]
		for _, t := range hits {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		s.redeemHits[ip] = kept
		if len(kept) >= limit {
			s.redeemMu.Unlock()
			writeError(w, http.StatusTooManyRequests, "操作过于频繁,请稍后再试")
			return
		}
		s.redeemHits[ip] = append(kept, now)
		s.redeemMu.Unlock()
		next(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"lastSweepAt": s.sweep.LastSweepAt,
	})
}

// handleCaptcha GET /captcha 返回验证码 PNG 与 id。
func (s *Server) handleCaptcha(w http.ResponseWriter, r *http.Request) {
	id, data, err := s.captcha.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "验证码生成失败")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Captcha-Id", id)
	w.Write(data)
}

// envelope 统一响应包裹(common/patterns.md API Response Format)。
type envelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(envelope{OK: status < 400, Data: data})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(envelope{OK: false, Error: msg})
}
