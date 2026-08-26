// Package verify 实现 derper 客户端准入回调。
// 协议(headscale issue #1953): derper POST {"NodePublic","Source"} 到
// /verify/<instanceID>,期望 200 {"Allow":bool}。
package verify

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"time"

	"derp-orch/internal/store"
)

// instanceIDPattern 路径参数格式校验,防注入。
var instanceIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// verifyRequest derper 发来的准入请求。
type verifyRequest struct {
	NodePublic string `json:"NodePublic"`
	Source     string `json:"Source"`
}

// verifyResponse 协议响应。
type verifyResponse struct {
	Allow bool `json:"Allow"`
}

// Handler per-instance 准入处理器。
type Handler struct {
	store *store.Store
	now   func() time.Time
}

// New 创建 verify handler。
func New(s *store.Store) *Handler {
	return &Handler{store: s, now: time.Now}
}

// ServeHTTP 处理 POST /verify/{instanceID}。
// 实例不存在、非 active 或已过期 → Allow:false。
// 200 恒返回协议 JSON;非法路径返回 400(derper 视为拒绝)。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("instanceID")
	if !instanceIDPattern.MatchString(id) {
		http.Error(w, "bad instance id", http.StatusBadRequest)
		return
	}

	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	inst, err := h.store.GetInstance(r.Context(), id)
	allow := false
	switch {
	case err == store.ErrNotFound:
		// 不存在 → 拒绝。
	case err != nil:
		// 存储错误:保守拒绝,并记录(不允许 fail-open)。
		log.Printf("[verify] 查询实例 %s 失败: %v", id, err)
	case inst.State != "active" || inst.Expired(h.now()):
		// 非活跃或过期 → 拒绝。
	default:
		allow = true
	}

	// 全量审计日志。
	log.Printf("[verify] instance=%s allow=%v nodePublic=%q source=%q", id, allow, req.NodePublic, req.Source)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(verifyResponse{Allow: allow})
}
