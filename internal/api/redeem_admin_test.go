package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"derp-orch/internal/auth"
	"derp-orch/internal/captcha"
	"derp-orch/internal/config"
	"derp-orch/internal/lifecycle"
	"derp-orch/internal/limits"
	"derp-orch/internal/store"
)

func newAdminServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		RelayBaseDomain: "wh.easylan.site",
		DefaultTTL:      24 * time.Hour,
		RegionIDBase:    900,
		STUNPortBase:    3478,
		AdminAPIKey:     "api-key",
		AdminUsername:   "admin",
		AdminPassword:   "secret",
		SessionTTL:      time.Hour,
		DERPPort:        443,
	}
	orch := &fakeOrch{}
	sweep := lifecycle.New(st, orch, time.Hour)
	captchaStore := captcha.New()
	authmgr := auth.New(auth.Options{
		Username: "admin", SessionTTL: time.Hour,
		LoginRateLimit: 100, LoginWindow: time.Minute, MaxFails: 5, LockDuration: time.Minute,
		Verify: func(p string) bool { return p == "secret" },
	})
	checker := limits.New(limits.Options{MaxContainers: 100, MemoryWatermarkPct: 80, CPUWatermarkPct: 80}, orch, st)
	s := &Server{cfg: cfg, store: st, orch: orch, sweep: sweep, captcha: captchaStore, authmgr: authmgr, limits: checker}
	s.redeemHits = map[string][]time.Time{}
	return s
}

// TestRedeemFlow_CodeThenRedeem 端到端:注入码 → 兑换 → 码绑定 → 二次兑换拒绝 → 审计 → 401。
func TestRedeemFlow_CodeThenRedeem(t *testing.T) {
	s := newAdminServer(t)
	ctx := context.Background()
	if _, err := s.store.CreateRedeemCodes(ctx, []store.RedeemCode{
		{Code: "DERP-TEST-0001", DurationValue: 30, DurationUnit: "day", CreatedBy: "admin", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}

	// 兑换(大小写不敏感)。
	body, _ := json.Marshal(map[string]string{"code": "derp-test-0001"})
	rec := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("兑换失败: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Instance store.Instance `json:"instance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || !env.OK {
		t.Fatalf("响应解析失败: %v %s", err, rec.Body.String())
	}
	instID := env.Data.Instance.ID
	if instID == "" {
		t.Fatal("实例 ID 为空")
	}

	// 码应已绑定实例。
	rc, err := s.store.GetRedeemCode(ctx, "DERP-TEST-0001")
	if err != nil || rc.UsedByInstanceID == nil || *rc.UsedByInstanceID != instID {
		t.Fatalf("码未绑定实例: %+v err=%v", rc, err)
	}
	// 行为变更(v2):已用码再兑改为"重取"语义,返回 200 + redeemed=true。
	// 容器被删 / 过期场景的 410 由后续 TestRedeem_UsedCodeInstanceDeleted/Expired 覆盖。
	rec2 := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec2, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("已用码应重取 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var env2 struct {
		Data struct {
			Redeemed bool            `json:"redeemed"`
			Instance store.Instance  `json:"instance"`
			DerpMap  json.RawMessage `json:"derpMap"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &env2); err != nil {
		t.Fatalf("重取响应解析失败: %v %s", err, rec2.Body.String())
	}
	if !env2.Data.Redeemed {
		t.Error("重取响应 redeemed 应为 true")
	}
	if env2.Data.Instance.ID != instID {
		t.Errorf("重取应返回同一实例: 期望 %s, 实际 %s", instID, env2.Data.Instance.ID)
	}
	if len(env2.Data.DerpMap) == 0 {
		t.Error("重取响应 derpMap 缺失")
	}
	// 审计日志应有一条 redeem。
	logs, err := s.store.ListAudit(ctx, 10)
	if err != nil || len(logs) == 0 || logs[0].Kind != "redeem" {
		t.Fatalf("审计日志缺失: %+v err=%v", logs, err)
	}
	// 无会话访问 admin API 应 401。
	unauth := httptest.NewRecorder()
	s.adminAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(
		unauth, httptest.NewRequest(http.MethodGet, "/v1/admin/stats", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("未登录访问 admin 应 401, got %d", unauth.Code)
	}
}

// TestCreateCodesHandler 建码(自定义前缀)+ 列表 + 导出。
func TestCreateCodesHandler(t *testing.T) {
	s := newAdminServer(t)

	body, _ := json.Marshal(map[string]any{
		"count": 3, "durationValue": 2, "durationUnit": "month", "prefix": "vip",
	})
	rec := httptest.NewRecorder()
	s.handleCreateCodes(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/codes", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("建码失败: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Created int                `json:"created"`
			Codes   []store.RedeemCode `json:"codes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Created != 3 {
		t.Fatalf("应建 3 个, got %d", env.Data.Created)
	}
	for _, c := range env.Data.Codes {
		if len(c.Code) < 8 || c.Code[:3] != "VIP" {
			t.Fatalf("前缀未生效: %q", c.Code)
		}
		if c.DurationUnit != "month" || c.DurationValue != 2 {
			t.Fatalf("时长错: %+v", c)
		}
	}

	// 列表。
	rec2 := httptest.NewRecorder()
	s.handleListCodes(rec2, httptest.NewRequest(http.MethodGet, "/v1/admin/codes?status=unused", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("列表失败: %d", rec2.Code)
	}
	// 导出 txt。
	rec3 := httptest.NewRecorder()
	s.handleExportCodes(rec3, httptest.NewRequest(http.MethodGet, "/v1/admin/codes/export?status=unused&format=txt", nil))
	if rec3.Code != http.StatusOK || len(rec3.Body.Bytes()) == 0 {
		t.Fatalf("导出失败: %d", rec3.Code)
	}
	// 导出 csv。
	rec4 := httptest.NewRecorder()
	s.handleExportCodes(rec4, httptest.NewRequest(http.MethodGet, "/v1/admin/codes/export?status=unused&format=csv", nil))
	if rec4.Code != http.StatusOK || len(rec4.Body.Bytes()) == 0 {
		t.Fatalf("CSV 导出失败: %d", rec4.Code)
	}
}

// TestLogin_FirstNoCaptchaThenRequired 首次登录免验证码;密码错后需验证码。
func TestLogin_FirstNoCaptchaThenRequired(t *testing.T) {
	s := newAdminServer(t)
	// 首次:正确密码无验证码 → 成功。
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret"})
	rec := httptest.NewRecorder()
	s.handleAdminLogin(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/login", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("首次免验证码登录应成功: %d %s", rec.Code, rec.Body.String())
	}

	// 密码错 → 401 且该 IP 被标记。
	bad, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/admin/login", bytes.NewReader(bad))
	req2.RemoteAddr = "10.0.0.1:1234"
	s.handleAdminLogin(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("错密码应 401: %d", rec2.Code)
	}
	if !s.authmgr.NeedsCaptcha("10.0.0.1") {
		t.Fatal("密码失败后该 IP 应需验证码")
	}

	// 同 IP 再次正确密码但无验证码 → 拒绝。
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/v1/admin/login", bytes.NewReader(body))
	req3.RemoteAddr = "10.0.0.1:1234"
	s.handleAdminLogin(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("失败后缺验证码应拒绝: %d", rec3.Code)
	}

	// 同 IP 正确密码 + 假验证码 → 拒绝。
	withCap, _ := json.Marshal(map[string]string{"username": "admin", "password": "secret", "captchaId": "x", "captchaText": "x"})
	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest(http.MethodPost, "/v1/admin/login", bytes.NewReader(withCap))
	req4.RemoteAddr = "10.0.0.1:1234"
	s.handleAdminLogin(rec4, req4)
	if rec4.Code != http.StatusBadRequest {
		t.Fatalf("假验证码应拒绝: %d", rec4.Code)
	}
}

// TestLoginState 登录状态端点返回 needCaptcha。
func TestLoginState(t *testing.T) {
	s := newAdminServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/login/state", nil)
	req.RemoteAddr = "10.0.0.9:1"
	s.handleAdminLoginState(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state 端点: %d", rec.Code)
	}
}

// TestChangePassword 改密码:旧密码错拒绝;成功后旧会话失效、旧密码失效。
func TestChangePassword(t *testing.T) {
	s := newAdminServer(t)
	ctx := context.Background()
	if err := s.store.InitAdminPassword(ctx, "seed"); err != nil {
		t.Fatal(err)
	}
	// 旧密码错。
	bad, _ := json.Marshal(map[string]string{"oldPassword": "nope", "newPassword": "newpass1"})
	rec := httptest.NewRecorder()
	s.handleAdminChangePassword(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/password", bytes.NewReader(bad)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("旧密码错应拒绝: %d", rec.Code)
	}
	// 正确改密。
	ok, _ := json.Marshal(map[string]string{"oldPassword": "seed", "newPassword": "newpass1"})
	rec2 := httptest.NewRecorder()
	s.handleAdminChangePassword(rec2, httptest.NewRequest(http.MethodPost, "/v1/admin/password", bytes.NewReader(ok)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("改密失败: %d %s", rec2.Code, rec2.Body.String())
	}
	if !s.store.VerifyAdminPassword(ctx, "newpass1") {
		t.Fatal("新密码应生效")
	}
	if s.store.VerifyAdminPassword(ctx, "seed") {
		t.Fatal("旧密码应失效")
	}
}

// redeemOnce 注入测试码 + 兑换一次,返回实例 ID。
func redeemOnce(t *testing.T, s *Server, code string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := s.store.CreateRedeemCodes(ctx, []store.RedeemCode{
		{Code: code, DurationValue: 30, DurationUnit: "day", CreatedBy: "admin", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	rec := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("首次兑换应 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Instance store.Instance `json:"instance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	return env.Data.Instance.ID
}

// TestRedeem_UsedCodeReturnsExistingInstance 已用码再兑:200 + redeemed=true + 同一 instID + derpMap。
func TestRedeem_UsedCodeReturnsExistingInstance(t *testing.T) {
	s := newAdminServer(t)
	instID := redeemOnce(t, s, "DERP-LOOKUP-0001")

	body, _ := json.Marshal(map[string]string{"code": "DERP-LOOKUP-0001"})
	rec := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("重取应 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Redeemed         bool            `json:"redeemed"`
			Instance         store.Instance  `json:"instance"`
			DerpMap          json.RawMessage `json:"derpMap"`
			SecondsRemaining int64           `json:"secondsRemaining"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if !env.Data.Redeemed {
		t.Error("redeemed 应为 true")
	}
	if env.Data.Instance.ID != instID {
		t.Errorf("实例 ID 不匹配: 期望 %s, 实际 %s", instID, env.Data.Instance.ID)
	}
	if len(env.Data.DerpMap) == 0 {
		t.Error("derpMap 缺失")
	}
	if env.Data.SecondsRemaining <= 0 {
		t.Errorf("secondsRemaining 应 > 0, 实际 %d", env.Data.SecondsRemaining)
	}
}

// TestRedeem_UsedCodeInstanceDeletedReturnsExpired 容器被管理员删后,重取 → 410。
func TestRedeem_UsedCodeInstanceDeletedReturnsExpired(t *testing.T) {
	s := newAdminServer(t)
	ctx := context.Background()
	instID := redeemOnce(t, s, "DERP-LOOKUP-0002")

	if err := s.store.DeleteInstance(ctx, instID); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"code": "DERP-LOOKUP-0002"})
	rec := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec.Code != http.StatusGone {
		t.Fatalf("应 410, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error == "" {
		t.Error("error 字段缺失")
	}
}

// TestRedeem_UsedCodeInstanceExpired 容器过期后,重取 → 410。
func TestRedeem_UsedCodeInstanceExpired(t *testing.T) {
	s := newAdminServer(t)
	ctx := context.Background()
	instID := redeemOnce(t, s, "DERP-LOOKUP-0003")

	if err := s.store.UpdateInstanceExpiresAt(ctx, instID, time.Now().Add(-1*time.Hour)); err != nil {
		t.Fatalf("UpdateInstanceExpiresAt: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"code": "DERP-LOOKUP-0003"})
	rec := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec.Code != http.StatusGone {
		t.Fatalf("应 410, got %d: %s", rec.Code, rec.Body.String())
	}
}
