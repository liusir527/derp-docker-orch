// Package api HTTP 层: 路由、认证、统一响应包裹。
package api

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"

	"derp-orch/internal/config"
	"derp-orch/internal/docker"
	"derp-orch/internal/lifecycle"
	"derp-orch/internal/store"
	"derp-orch/internal/verify"
)

// Server API 服务。组合全部依赖。
type Server struct {
	cfg    *config.Config
	store  *store.Store
	orch   docker.Orchestrator
	sweep  *lifecycle.Sweeper
	verify *verify.Handler
}

// New 构建路由。
func New(cfg *config.Config, st *store.Store, orch docker.Orchestrator, sweep *lifecycle.Sweeper) http.Handler {
	s := &Server{cfg: cfg, store: st, orch: orch, sweep: sweep, verify: verify.New(st)}
	mux := http.NewServeMux()

	// 公开端点: Web 页面、健康检查、derper 准入回调。
	mux.HandleFunc("GET /", s.handleWeb)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("POST /verify/{instanceID}", s.verify)

	// 管理端点: Bearer 认证。
	mux.Handle("POST /v1/instances", s.auth(http.HandlerFunc(s.handleCreateInstance)))
	mux.Handle("GET /v1/instances", s.auth(http.HandlerFunc(s.handleListInstances)))
	mux.Handle("GET /v1/instances/{id}", s.auth(http.HandlerFunc(s.handleGetInstance)))
	mux.Handle("DELETE /v1/instances/{id}", s.auth(http.HandlerFunc(s.handleDeleteInstance)))

	return mux
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

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"lastSweepAt": s.sweep.LastSweepAt,
	})
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
