package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"derp-orch/internal/derp"
	"derp-orch/internal/store"
)

const (
	maxNameAttempts = 10
	maxTTLHours     = 168 // 7 天
	minTTLHours     = 1
)

// createInstanceRequest 创建请求。
type createInstanceRequest struct {
	// TTLHours 有效期(小时),1-168,缺省用平台默认。
	TTLHours int `json:"ttlHours"`
}

// createInstanceResponse 创建响应。
type createInstanceResponse struct {
	Instance *store.Instance `json:"instance"`
	DerpMap  json.RawMessage `json:"derpMap"`
}

// handleCreateInstance POST /v1/instances。
// 流程: 命名(唯一重试)→ 分配 regionID/stunPort → 插行 → 起容器 。
// 容器失败补偿回滚删行,不留幽灵资源。
func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	var req createInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}

	ttl := s.cfg.DefaultTTL
	if req.TTLHours != 0 {
		if req.TTLHours < minTTLHours || req.TTLHours > maxTTLHours {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("ttlHours 必须在 %d-%d 之间", minTTLHours, maxTTLHours))
			return
		}
		ttl = time.Duration(req.TTLHours) * time.Hour
	}

	inst, err := s.createInstance(r, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	derpMap, err := derp.MapForInstance(inst, s.cfg.DERPPort)
	if err != nil {
		// derpMap 生成失败是内部错误;实例已存在,允许通过 GET 重试。
		log.Printf("[api] 生成实例 %s derpMap 失败: %v", inst.ID, err)
		writeError(w, http.StatusInternalServerError, "生成 derpMap 失败")
		return
	}
	writeJSON(w, http.StatusCreated, createInstanceResponse{Instance: inst, DerpMap: derpMap})
}

// createInstance 完整创建流程,含命名唯一重试与失败补偿。
func (s *Server) createInstance(r *http.Request, ttl time.Duration) (*store.Instance, error) {
	ctx := r.Context()
	now := time.Now()

	var lastErr error
	for attempt := 0; attempt < maxNameAttempts; attempt++ {
		name, err := derp.GenerateName(now)
		if err != nil {
			return nil, err
		}
		inst := &store.Instance{
			ID:            name,
			Name:          name,
			Subdomain:     s.cfg.Subdomain(name),
			RegionCode:    regionCodeFromName(name),
			ContainerName: name,
			ExpiresAt:     now.Add(ttl),
			State:         "active",
			CreatedAt:     now,
		}
		// 单事务内完成 regionID/stunPort 分配 + 插入;命名冲突(并发同秒)→ 换名重试。
		if err := s.store.CreateInstanceAllocated(ctx, inst, s.cfg.RegionIDBase, s.cfg.STUNPortBase); err != nil {
			lastErr = err
			if isUniqueViolation(err) {
				continue
			}
			return nil, err
		}

		// 容器编排;失败则补偿删行,释放资源。
		if err := s.orch.EnsureNode(ctx, inst); err != nil {
			if derr := s.store.DeleteInstance(ctx, inst.ID); derr != nil {
				log.Printf("[api] 补偿删除实例 %s 失败: %v", inst.ID, derr)
			}
			return nil, fmt.Errorf("启动容器失败(已回滚): %w", err)
		}
		// 容器创建成功后更新 HostIP。
		if err := s.store.UpdateInstanceHostIP(ctx, inst.ID, inst.HostIP); err != nil {
			log.Printf("[api] 更新 HostIP 失败: %v", err)
		}
		log.Printf("[api] 创建实例 %s(region=%d stun=%d ttl=%s)", inst.ID, inst.RegionID, inst.STUNPort, ttl)
		return inst, nil
	}
	return nil, fmt.Errorf("命名重试 %d 次仍冲突: %v", maxNameAttempts, lastErr)
}

// handleListInstances GET /v1/instances。
func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListInstances(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetInstance GET /v1/instances/{id}。附容器状态与剩余秒数。
func (s *Server) handleGetInstance(w http.ResponseWriter, r *http.Request) {
	inst, err := s.store.GetInstance(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "实例不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state, err := s.orch.NodeState(r.Context(), inst)
	if err != nil {
		// 容器查询失败不阻塞详情返回,标记未知。
		log.Printf("[api] 查询实例 %s 容器状态失败: %v", inst.ID, err)
	}
	remaining := int64(0)
	if rem := time.Until(inst.ExpiresAt); rem > 0 {
		remaining = int64(rem.Seconds())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instance":         inst,
		"containerState":   state,
		"secondsRemaining": remaining,
	})
}

// handleDeleteInstance DELETE /v1/instances/{id}。幂等:不存在也返回成功。
func (s *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	inst, err := s.store.GetInstance(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 先停容器,再删行;容器失败保留行供重试。
	if err := s.orch.RemoveNode(ctx, inst); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("移除容器失败: %v", err))
		return
	}
	if err := s.store.DeleteInstance(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[api] 删除实例 %s", id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// regionCodeFromName 从 derp-YYYYMMDD-xxxx 提取短日期段(YYYYMMDD[2:],如 260812)。
func regionCodeFromName(name string) string {
	if len(name) >= 13 {
		return name[7:13]
	}
	return "derp"
}

// isUniqueViolation 判定 SQLite 唯一约束冲突。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
