package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// 单次编排操作的超时。拉镜像可能较慢,给足时间。
const opTimeout = 120 * time.Second

// Server regiond 的 HTTP API。所有 /v1/* 请求需 Bearer Token。
type Server struct {
	orch  docker.Orchestrator
	token string
}

// NewServer 创建代理服务。token 为空时拒绝一切 /v1 请求(启动时应校验非空)。
func NewServer(orch docker.Orchestrator, token string) *Server {
	return &Server{orch: orch, token: token}
}

// Handler 返回带路由与鉴权的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.Handle("GET /v1/info", s.auth(http.HandlerFunc(s.handleInfo)))
	mux.Handle("GET /v1/stats", s.auth(http.HandlerFunc(s.handleStats)))
	mux.Handle("POST /v1/nodes", s.auth(http.HandlerFunc(s.handleEnsureNode)))
	mux.Handle("DELETE /v1/nodes/{name}", s.auth(http.HandlerFunc(s.handleRemoveNode)))
	mux.Handle("GET /v1/nodes/{name}", s.auth(http.HandlerFunc(s.handleNodeState)))
	return mux
}

// auth Bearer Token 校验。常量时间比较,避免时序侧信道。
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			writeError(w, http.StatusServiceUnavailable, "regiond 未配置 AGENT_TOKEN")
			return
		}
		got := r.Header.Get("Authorization")
		want := "Bearer " + s.token
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeError(w, http.StatusUnauthorized, "无效的代理令牌")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	info := docker.HostInfo{}
	if hi, ok := s.orch.(docker.HostInfoer); ok {
		if got, err := hi.HostInfo(ctx); err == nil {
			info = got
		}
	}
	dockerOK := true
	if p, ok := s.orch.(interface{ Ping(context.Context) error }); ok {
		dockerOK = p.Ping(ctx) == nil
	}
	writeJSON(w, http.StatusOK, InfoResponse{HostInfo: info, DockerOK: dockerOK})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	stats, err := s.orch.Stats(ctx)
	if err != nil {
		log.Printf("[regiond] stats 失败: %v", err)
		writeError(w, http.StatusInternalServerError, "采集容器资源失败")
		return
	}
	writeJSON(w, http.StatusOK, StatsResponse{Stats: stats})
}

func (s *Server) handleEnsureNode(w http.ResponseWriter, r *http.Request) {
	var req EnsureNodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Instance == nil || req.Instance.ContainerName == "" || req.Instance.Subdomain == "" {
		writeError(w, http.StatusBadRequest, "instance.containerName/subdomain 必填")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()
	if err := s.orch.EnsureNode(ctx, req.Instance); err != nil {
		log.Printf("[regiond] 创建容器 %s 失败: %v", req.Instance.ContainerName, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[regiond] 容器 %s 已就绪(地区 %s)", req.Instance.ContainerName, req.Instance.Region)
	writeJSON(w, http.StatusOK, NodeResponse{State: docker.StateRunning, HostIP: req.Instance.HostIP})
}

func (s *Server) handleRemoveNode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "容器名必填")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), opTimeout)
	defer cancel()
	if err := s.orch.RemoveNode(ctx, &store.Instance{ID: name, ContainerName: name}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleNodeState(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "容器名必填")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	state, err := s.orch.NodeState(ctx, &store.Instance{ID: name, ContainerName: name})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, NodeResponse{State: state})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
