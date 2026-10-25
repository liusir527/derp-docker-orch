package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"derp-orch/internal/derp"
	"derp-orch/internal/redeem"
	"derp-orch/internal/store"
)

// redeemRequest 兑换请求。
type redeemRequest struct {
	Code string `json:"code"`
	// Region 目标地区 slug(用户兑换时自选),空表示本地地区。
	Region string `json:"region"`
}

// handleRedeem POST /v1/redeem/redeem。
// 流程: 限速 → 读码 → 判已用 →
//
//	(已用 + 实例在 + 未过期)→ 重取 200 + redeemed=true
//	(已用 + 实例过期/被删)   → 410
//	(未用)                  → 配额检查 → reserve 码 → 建实例 → consume 码 → 201 + redeemed=false
//
// 任一步失败:release 码 + 补偿删行,不留半状态。
func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	var req redeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	code := redeem.NormalizeCode(req.Code)
	if code == "" {
		writeError(w, http.StatusBadRequest, "请输入兑换码")
		return
	}

	// 一次性读码:同时承担"码不存在"与"已用"两种判定。
	rc, err := s.store.GetRedeemCode(r.Context(), code)
	if err != nil {
		if errors.Is(err, store.ErrCodeUnavailable) {
			writeError(w, http.StatusBadRequest, "兑换码无效或已被使用")
			return
		}
		writeError(w, http.StatusInternalServerError, "兑换码处理失败")
		return
	}
	if rc.Disabled {
		writeError(w, http.StatusBadRequest, "兑换码无效或已被使用")
		return
	}

	// 已用码分支:重取 derpMap,只读、不消耗资源,跳过配额。
	if rc.Used() {
		s.handleRedeemLookup(w, r, code, rc)
		return
	}

	// 解析目标地区(用户自选)。已用码在上一分支已返回,不受此影响。
	reg, err := s.regionFor(r.Context(), req.Region)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 配额准入:防无限量创建(按地区统计)。
	ok, reason, _, err := s.limits.CanCreate(r.Context(), reg.ID)
	if err != nil {
		log.Printf("[redeem] 资源评估失败: %v", err)
		writeError(w, http.StatusServiceUnavailable, "资源评估暂时不可用,请稍后再试")
		return
	}
	if !ok {
		writeError(w, http.StatusTooManyRequests, reason)
		return
	}

	// 原子抢占码(15min TTL)。
	token, err := s.store.ReserveRedeemCode(r.Context(), code)
	if err != nil {
		if errors.Is(err, store.ErrCodeUnavailable) {
			writeError(w, http.StatusBadRequest, "兑换码无效或已被使用")
			return
		}
		writeError(w, http.StatusInternalServerError, "兑换码处理失败")
		return
	}
	release := func() {
		if rerr := s.store.ReleaseRedeemCode(r.Context(), token); rerr != nil {
			log.Printf("[redeem] 释放码预留失败: %v", rerr)
		}
	}

	// 读取码时长。
	rcc, err := s.store.GetRedeemCodeByToken(r.Context(), token)
	if err != nil {
		release()
		writeError(w, http.StatusBadRequest, "兑换码无效或已被使用")
		return
	}
	expiry, err := redeem.ExpiryFrom(time.Now(), rcc.DurationValue, rcc.DurationUnit)
	if err != nil {
		release()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ttl := time.Until(expiry)

	// 建实例(复用既有流程,补偿删行内建)。
	inst, err := s.createInstance(r, ttl, reg)
	if err != nil {
		release()
		log.Printf("[redeem] 创建实例失败: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 消费码绑定实例。
	if err := s.store.ConsumeRedeemCode(r.Context(), token, inst.ID); err != nil {
		// 码消费失败:删实例防孤儿,释放码。
		if derr := s.store.DeleteInstance(r.Context(), inst.ID); derr != nil {
			log.Printf("[redeem] 补偿删除实例 %s 失败: %v", inst.ID, derr)
		}
		release()
		log.Printf("[redeem] 消费码 %s 失败: %v", code, err)
		writeError(w, http.StatusInternalServerError, "兑换码消费失败")
		return
	}

	derpMap, _ := s.buildDerpMap(r.Context(), inst)

	// 审计:码→实例绑定 + 创建/过期时间(分钟级)。
	_ = s.store.AddAudit(r.Context(), store.AuditLog{
		Kind:       "redeem",
		InstanceID: inst.ID,
		Code:       code,
		Detail:     "兑换创建实例 地区=" + reg.Name + " 创建时间=" + inst.CreatedAt.Format("2006-01-02 15:04") + " 过期时间=" + inst.ExpiresAt.Format("2006-01-02 15:04"),
	})

	log.Printf("[redeem] 兑换成功 code=%s 地区=%s instance=%s expires=%s", code, reg.ID, inst.ID, inst.ExpiresAt.Format(time.RFC3339))
	writeJSON(w, http.StatusCreated, map[string]any{
		"instance":         inst,
		"region":           regionPayload(reg),
		"derpMap":          derpMap,
		"expiresAt":        inst.ExpiresAt,
		"secondsRemaining": int64(time.Until(inst.ExpiresAt).Seconds()),
		"redeemed":         false,
	})
}

// handleRedeemLookup 已用码重取分支。容器不存在或已过期 → 410 Gone。
// 重取是只读操作,不写审计。
func (s *Server) handleRedeemLookup(w http.ResponseWriter, r *http.Request, code string, rc *store.RedeemCode) {
	if rc.UsedByInstanceID == nil {
		// 防御:schema 约束 used_by_instance_id 与 used_at 同步写入,理论上不应出现。
		log.Printf("[redeem] 码 %s 状态异常:used_at 非空但 used_by_instance_id 为空", code)
		writeError(w, http.StatusInternalServerError, "兑换码处理失败")
		return
	}
	inst, err := s.store.GetInstance(r.Context(), *rc.UsedByInstanceID)
	if err != nil {
		// 实例被管理员删除 → 等同过期语义。
		writeError(w, http.StatusGone, "兑换码对应的中继已过期或已被清理")
		return
	}
	if inst.Expired(time.Now()) {
		writeError(w, http.StatusGone, "兑换码对应的中继已过期或已被清理")
		return
	}

	derpMap, _ := s.buildDerpMap(r.Context(), inst)
	regionName := inst.Region
	if reg, err := s.store.GetRegion(r.Context(), inst.Region); err == nil {
		regionName = reg.Name
	}
	log.Printf("[redeem] 重取已用码 code=%s 地区=%s instance=%s expires=%s", code, inst.Region, inst.ID, inst.ExpiresAt.Format(time.RFC3339))
	writeJSON(w, http.StatusOK, map[string]any{
		"instance":         inst,
		"region":           map[string]any{"id": inst.Region, "name": regionName},
		"derpMap":          derpMap,
		"expiresAt":        inst.ExpiresAt,
		"secondsRemaining": int64(time.Until(inst.ExpiresAt).Seconds()),
		"redeemed":         true,
	})
}

// regionPayload 响应里的地区展示信息。
func regionPayload(reg *store.Region) map[string]any {
	return map[string]any{"id": reg.ID, "name": reg.Name, "baseDomain": reg.BaseDomain}
}

// buildDerpMap 序列化 derpMap JSON,失败回退空对象(沿用原 redeem 行为)。
func (s *Server) buildDerpMap(_ context.Context, inst *store.Instance) (json.RawMessage, error) {
	m, err := derp.MapForInstance(inst, s.cfg.DERPPort)
	if err != nil {
		log.Printf("[redeem] 生成实例 %s derpMap 失败: %v", inst.ID, err)
		return json.RawMessage("{}"), nil
	}
	return json.RawMessage(m), nil
}
