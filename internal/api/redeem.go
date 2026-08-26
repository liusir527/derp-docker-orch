package api

import (
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
}

// handleRedeem POST /v1/redeem/redeem。
// 流程: 限速 → 配额检查 → reserve 码 → 建实例(ttl=码时长)→ consume 码。
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

	// 配额准入:防无限量创建。
	ok, reason, _, err := s.limits.CanCreate(r.Context())
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
	rc, err := s.store.GetRedeemCodeByToken(r.Context(), token)
	if err != nil {
		release()
		writeError(w, http.StatusBadRequest, "兑换码无效或已被使用")
		return
	}
	expiry, err := redeem.ExpiryFrom(time.Now(), rc.DurationValue, rc.DurationUnit)
	if err != nil {
		release()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ttl := time.Until(expiry)

	// 建实例(复用既有流程,补偿删行内建)。
	inst, err := s.createInstance(r, ttl)
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

	derpMap, err := derp.MapForInstance(inst, s.cfg.DERPPort)
	if err != nil {
		log.Printf("[redeem] 生成 derpMap 失败: %v", err)
		derpMap = []byte("{}")
	}

	// 审计:码→实例绑定 + 创建/过期时间(分钟级)。
	_ = s.store.AddAudit(r.Context(), store.AuditLog{
		Kind:       "redeem",
		InstanceID: inst.ID,
		Code:       code,
		Detail:     "兑换创建实例 创建时间=" + inst.CreatedAt.Format("2006-01-02 15:04") + " 过期时间=" + inst.ExpiresAt.Format("2006-01-02 15:04"),
	})

	log.Printf("[redeem] 兑换成功 code=%s instance=%s expires=%s", code, inst.ID, inst.ExpiresAt.Format(time.RFC3339))
	writeJSON(w, http.StatusCreated, map[string]any{
		"instance":         inst,
		"derpMap":          json.RawMessage(derpMap),
		"expiresAt":        inst.ExpiresAt,
		"secondsRemaining": int64(time.Until(inst.ExpiresAt).Seconds()),
	})
}
