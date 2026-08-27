package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"derp-orch/internal/auth"
	"derp-orch/internal/derp"
	"derp-orch/internal/docker"
	"derp-orch/internal/redeem"
	"derp-orch/internal/store"
)

const maxBatchCodes = 1000

// adminLoginRequest 登录请求。
// 首次登录无需验证码(captchaId/captchaText 可空);密码失败后该 IP 再登录需验证码。
type adminLoginRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	CaptchaID   string `json:"captchaId"`
	CaptchaText string `json:"captchaText"`
}

// handleAdminLogin POST /v1/admin/login。
// 验证码规则:首次(该 IP 无失败记录)免验证码;失败后需要。
// 密码错误 → 标记该 IP 需验证码;成功 → 清除标记。
func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req adminLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}

	ip := auth.ClientIP(r)
	if !s.authmgr.CheckIPRate(ip) {
		writeError(w, http.StatusTooManyRequests, "登录过于频繁,请稍后再试")
		return
	}
	if locked, remain := s.authmgr.Locked(req.Username); locked {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("账户已锁定,请 %.0f 分钟后再试", remain.Minutes()))
		return
	}

	// 密码先校验(免验证码时也需先验密码)。
	token, err := s.authmgr.Login(req.Username, req.Password)
	if err != nil {
		// 密码错误:标记该 IP 后续需验证码。
		s.authmgr.MarkIPFailed(ip)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	// 密码正确,若该 IP 曾失败过则必须验证码。
	if s.authmgr.NeedsCaptcha(ip) {
		if !s.captcha.Validate(req.CaptchaID, req.CaptchaText) {
			writeError(w, http.StatusBadRequest, "验证码错误或已失效")
			return
		}
	}

	// 登录成功:清除失败标记 + 发会话 cookie。
	s.authmgr.ResetIPFailure(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     authSessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminLoginState GET /v1/admin/login/state。前端查询该 IP 是否需验证码。
func (s *Server) handleAdminLoginState(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r)
	writeJSON(w, http.StatusOK, map[string]any{"needCaptcha": s.authmgr.NeedsCaptcha(ip)})
}

// adminChangePasswordRequest 改密码请求。
type adminChangePasswordRequest struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// handleAdminChangePassword POST /v1/admin/password(需登录)。改密码后登出。
func (s *Server) handleAdminChangePassword(w http.ResponseWriter, r *http.Request) {
	var req adminChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	if req.NewPassword == "" || len(req.NewPassword) < 6 {
		writeError(w, http.StatusBadRequest, "新密码至少 6 位")
		return
	}
	if err := s.store.UpdateAdminPassword(r.Context(), req.OldPassword, req.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, "旧密码错误或记录不存在")
		return
	}
	// 使当前会话失效。
	if c, cerr := r.Cookie(authSessionCookie); cerr == nil {
		s.authmgr.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: authSessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "relogin": true})
}

// handleAdminLogout POST /v1/admin/logout。
func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(authSessionCookie); err == nil {
		s.authmgr.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: authSessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// createCodesRequest 建码请求。prefix 可选自定义前缀。
type createCodesRequest struct {
	Count         int    `json:"count"`
	DurationValue int    `json:"durationValue"`
	DurationUnit  string `json:"durationUnit"`
	Prefix        string `json:"prefix,omitempty"`
	Note          string `json:"note,omitempty"`
}

// handleCreateCodes POST /v1/admin/codes。
func (s *Server) handleCreateCodes(w http.ResponseWriter, r *http.Request) {
	var req createCodesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	if req.Count <= 0 || req.Count > maxBatchCodes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("数量必须在 1-%d 之间", maxBatchCodes))
		return
	}
	if _, _, err := redeem.ValidateDuration(req.DurationValue, req.DurationUnit); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	prefix := redeem.NormalizePrefix(req.Prefix)

	now := time.Now()
	var codes []store.RedeemCode
	for i := 0; i < req.Count; i++ {
		code, err := redeem.GenerateCode(prefix)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		codes = append(codes, store.RedeemCode{
			Code:          code,
			DurationValue: req.DurationValue,
			DurationUnit:  req.DurationUnit,
			CreatedBy:     "admin",
			CreatedAt:     now,
			Note:          req.Note,
		})
	}
	created, err := s.store.CreateRedeemCodes(r.Context(), codes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), store.AuditLog{
		Kind:   "create_code",
		Code:   prefix,
		Detail: fmt.Sprintf("批量创建 %d 个兑换码 时长=%d%s 前缀=%s", len(created), req.DurationValue, req.DurationUnit, prefix),
	})
	log.Printf("[admin] 批量创建 %d 个兑换码 前缀=%s 时长=%d%s", len(created), prefix, req.DurationValue, req.DurationUnit)
	writeJSON(w, http.StatusCreated, map[string]any{
		"created": len(created),
		"codes":   created,
	})
}

// handleListCodes GET /v1/admin/codes?status=&limit=&offset=。
func (s *Server) handleListCodes(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "all"
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	codes, total, err := s.store.ListRedeemCodes(r.Context(), status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes, "total": total})
}

// handleExportCodes GET /v1/admin/codes/export?status=unused&format=txt|csv。
func (s *Server) handleExportCodes(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "unused"
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "txt"
	}
	codes, _, err := s.store.ListRedeemCodes(r.Context(), status, 0, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ts := time.Now().Format("20060102-1504")
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="redeem-codes-%s.csv"`, ts))
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"code", "duration_value", "duration_unit", "created_at", "note"})
		for _, c := range codes {
			_ = cw.Write([]string{c.Code, strconv.Itoa(c.DurationValue), c.DurationUnit,
				c.CreatedAt.Format("2006-01-02 15:04"), c.Note})
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="redeem-codes-%s.txt"`, ts))
		var b strings.Builder
		for _, c := range codes {
			b.WriteString(c.Code)
			b.WriteByte('\n')
		}
		_, _ = w.Write([]byte(b.String()))
	}
}

// handleAdminStats GET /v1/admin/stats。容器数 + 资源 + 码用量。
func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	insts, err := s.store.ListInstances(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	active := 0
	for _, i := range insts {
		if i.State == "active" {
			active++
		}
	}
	totalCodes, unusedCodes, usedCodes, err := s.store.CountRedeemCodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rep, err := s.limits.Report(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instances":       len(insts),
		"activeInstances": active,
		"codes":           map[string]int{"total": totalCodes, "unused": unusedCodes, "used": usedCodes},
		"resources":       rep,
	})
}

// handleAdminInstances GET /v1/admin/instances。实例 + 各容器资源。
func (s *Server) handleAdminInstances(w http.ResponseWriter, r *http.Request) {
	insts, err := s.store.ListInstances(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stats, err := s.orch.Stats(r.Context())
	if err != nil {
		stats = map[string]*docker.ContainerStat{}
	}
	type row struct {
		*store.Instance
		ContainerState string  `json:"containerState"`
		CPUPct         float64 `json:"cpuPct"`
		MemBytes       uint64  `json:"memBytes"`
	}
	out := make([]row, 0, len(insts))
	for _, i := range insts {
		st := stats[i.ContainerName]
		rw := row{Instance: i, ContainerState: "missing"}
		if st != nil {
			rw.ContainerState = "running"
			rw.CPUPct = st.CPUPct
			rw.MemBytes = st.MemBytes
		}
		out = append(out, rw)
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": out})
}

// handleAdminAudit GET /v1/admin/audit?limit=。
func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 200)
	logs, err := s.store.ListAudit(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 时间格式化为分钟级展示。
	type auditView struct {
		ID         int64  `json:"id"`
		Kind       string `json:"kind"`
		InstanceID string `json:"instanceId,omitempty"`
		Code       string `json:"code,omitempty"`
		Username   string `json:"username,omitempty"`
		Detail     string `json:"detail,omitempty"`
		CreatedAt  string `json:"createdAt"`
	}
	out := make([]auditView, 0, len(logs))
	for _, l := range logs {
		out = append(out, auditView{
			ID: l.ID, Kind: l.Kind, InstanceID: l.InstanceID, Code: l.Code,
			Username: l.Username, Detail: l.Detail,
			CreatedAt: l.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": out})
}

// handleAdminDeleteInstance DELETE /v1/admin/instances/{id}。管理员手动删除,带审计。
// 二次确认 UX 由前端模态框负责;后端仅校验路径 id。幂等:不存在 → {deleted:false}。
func (s *Server) handleAdminDeleteInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	inst, existed, err := s.deleteInstance(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !existed {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": false})
		return
	}

	// 审计:记录操作者与实例元数据,供事后追溯。
	username, _ := s.authmgr.Authenticate(r)
	detail := fmt.Sprintf("admin 手动删除实例 subdomain=%s regionID=%d stunPort=%d",
		inst.Subdomain, inst.RegionID, inst.STUNPort)
	if uerr := s.store.AddAudit(ctx, store.AuditLog{
		Kind:       "admin_delete_instance",
		InstanceID: inst.ID,
		Username:   username,
		Detail:     detail,
	}); uerr != nil {
		log.Printf("[admin] 写审计失败 instance=%s: %v", inst.ID, uerr)
	}
	log.Printf("[admin] 删除实例 %s 操作者=%s", inst.ID, username)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleAdminDerpMap GET /v1/admin/instances/{id}/derpmap。供管理员重取 derpMap。
func (s *Server) handleAdminDerpMap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	inst, err := s.store.GetInstance(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "实例不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	derpMap, err := derp.MapForInstance(inst, s.cfg.DERPPort)
	if err != nil {
		log.Printf("[admin] 生成实例 %s derpMap 失败: %v", inst.ID, err)
		writeError(w, http.StatusInternalServerError, "生成 derpMap 失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"derpMap": json.RawMessage(derpMap)})
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil && v >= 0 {
		return v
	}
	return def
}
