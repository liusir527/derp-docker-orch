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
		Username: "admin", Password: "secret", SessionTTL: time.Hour,
		LoginRateLimit: 100, LoginWindow: time.Minute, MaxFails: 5, LockDuration: time.Minute,
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
	// 再兑同码应失败。
	rec2 := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec2, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("已用码应拒绝, got %d", rec2.Code)
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
