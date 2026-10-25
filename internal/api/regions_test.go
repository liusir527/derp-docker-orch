package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"derp-orch/internal/agent"
	"derp-orch/internal/limits"
	"derp-orch/internal/region"
	"derp-orch/internal/store"
)

func seedRegion(t *testing.T, s *Server, reg *store.Region) {
	t.Helper()
	if reg.CreatedAt.IsZero() {
		reg.CreatedAt = time.Now()
	}
	if err := s.store.CreateRegion(context.Background(), reg); err != nil {
		t.Fatalf("CreateRegion(%s): %v", reg.ID, err)
	}
}

func TestPublicRegions_OnlyEnabled(t *testing.T) {
	s := newAdminServer(t)
	seedRegion(t, s, &store.Region{ID: "wh", Name: "华中", BaseDomain: "wh.easylan.site", RegionCode: "wh", Enabled: true})
	seedRegion(t, s, &store.Region{ID: "cd", Name: "西南", BaseDomain: "cd.easylan.site", RegionCode: "cd", Enabled: true})
	seedRegion(t, s, &store.Region{ID: "off", Name: "停用", BaseDomain: "off.easylan.site", RegionCode: "off", Enabled: false})

	rec := httptest.NewRecorder()
	s.handlePublicRegions(rec, httptest.NewRequest(http.MethodGet, "/v1/regions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Regions []regionView `json:"regions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || !env.OK {
		t.Fatalf("解析失败: %v %s", err, rec.Body.String())
	}
	if len(env.Data.Regions) != 2 {
		t.Fatalf("应只返回 2 个启用地区, got %d: %+v", len(env.Data.Regions), env.Data.Regions)
	}
	for _, r := range env.Data.Regions {
		if r.ID == "off" {
			t.Error("停用地区不应出现在公开列表")
		}
		if r.BaseDomain == "" {
			t.Errorf("地区 %s 缺 baseDomain", r.ID)
		}
	}
}

func TestRedeem_WithSelectedRegion(t *testing.T) {
	s := newAdminServer(t)
	seedRegion(t, s, &store.Region{ID: "cd", Name: "西南", BaseDomain: "cd.easylan.site", RegionCode: "cd", Enabled: true})
	ctx := context.Background()
	if _, err := s.store.CreateRedeemCodes(ctx, []store.RedeemCode{
		{Code: "DERP-REGION-0001", DurationValue: 30, DurationUnit: "day", CreatedBy: "admin", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"code": "DERP-REGION-0001", "region": "cd"})
	rec := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("兑换失败 %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Instance store.Instance `json:"instance"`
			Region   struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"region"`
			DerpMap json.RawMessage `json:"derpMap"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if env.Data.Instance.Region != "cd" {
		t.Errorf("实例地区应为 cd, got %q", env.Data.Instance.Region)
	}
	if !strings.HasSuffix(env.Data.Instance.Subdomain, ".relay.cd.easylan.site") {
		t.Errorf("子域名应用地区域名, got %q", env.Data.Instance.Subdomain)
	}
	if env.Data.Instance.RegionCode != "cd" {
		t.Errorf("regionCode 应取地区配置, got %q", env.Data.Instance.RegionCode)
	}
	if env.Data.Region.Name != "西南" {
		t.Errorf("响应地区名应为 西南, got %q", env.Data.Region.Name)
	}
	var dm struct {
		Regions map[string]struct {
			RegionCode string `json:"RegionCode"`
			Nodes      []struct {
				HostName string `json:"HostName"`
			} `json:"Nodes"`
		} `json:"Regions"`
	}
	if err := json.Unmarshal(env.Data.DerpMap, &dm); err != nil {
		t.Fatalf("derpMap 解析失败: %v (%s)", err, env.Data.DerpMap)
	}
	if len(dm.Regions) != 1 {
		t.Fatalf("derpMap 应含 1 个地区, got %d", len(dm.Regions))
	}
	for _, r := range dm.Regions {
		if r.RegionCode != "cd" {
			t.Errorf("derpMap RegionCode 应为 cd, got %q", r.RegionCode)
		}
		if len(r.Nodes) != 1 || !strings.HasSuffix(r.Nodes[0].HostName, ".relay.cd.easylan.site") {
			t.Errorf("derpMap 节点域名错误: %+v", r.Nodes)
		}
	}
}

func TestRedeem_RegionUnavailable(t *testing.T) {
	s := newAdminServer(t)
	seedRegion(t, s, &store.Region{ID: "off", Name: "停用", BaseDomain: "off.easylan.site", RegionCode: "off", Enabled: false})
	ctx := context.Background()
	if _, err := s.store.CreateRedeemCodes(ctx, []store.RedeemCode{
		{Code: "DERP-REGION-0002", DurationValue: 30, DurationUnit: "day", CreatedBy: "admin", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ region, wantErr string }{
		{"off", "地区已停用"},
		{"nowhere", "地区不存在"},
	}
	for _, tc := range cases {
		body, _ := json.Marshal(map[string]string{"code": "DERP-REGION-0002", "region": tc.region})
		rec := httptest.NewRecorder()
		s.rateLimit(s.handleRedeem)(rec, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("地区 %s 应 400, got %d: %s", tc.region, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.wantErr) {
			t.Errorf("错误信息应含 %q, got %s", tc.wantErr, rec.Body.String())
		}
	}
	// 失败后码应仍可用。
	rc, err := s.store.GetRedeemCode(ctx, "DERP-REGION-0002")
	if err != nil || rc.Used() {
		t.Fatalf("地区不可用不应消耗码: %+v err=%v", rc, err)
	}
}

func TestAdminRegion_CRUD(t *testing.T) {
	s := newAdminServer(t)
	seedRegion(t, s, &store.Region{ID: "wh", Name: "华中", BaseDomain: "wh.easylan.site", RegionCode: "wh", Enabled: true})

	// 新建远程地区。
	body, _ := json.Marshal(map[string]any{
		"id": "cd", "name": "西南", "baseDomain": "cd.easylan.site", "regionCode": "cd",
		"agentUrl": "https://agent.cd.easylan.site", "agentToken": "tok", "maxContainers": 50,
	})
	rec := httptest.NewRecorder()
	s.handleAdminCreateRegion(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/regions", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("新建地区失败 %d: %s", rec.Code, rec.Body.String())
	}
	reg, err := s.store.GetRegion(context.Background(), "cd")
	if err != nil || reg.AgentToken != "tok" || reg.MaxContainers != 50 {
		t.Fatalf("地区未正确落库: %+v err=%v", reg, err)
	}

	// 重复 id → 409。
	rec = httptest.NewRecorder()
	s.handleAdminCreateRegion(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/regions", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复地区应 409, got %d", rec.Code)
	}

	// 缺少代理令牌 → 400。
	bad, _ := json.Marshal(map[string]any{"id": "gz", "name": "华南", "baseDomain": "gz.easylan.site", "regionCode": "gz", "agentUrl": "https://agent.gz.easylan.site"})
	rec = httptest.NewRecorder()
	s.handleAdminCreateRegion(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/regions", bytes.NewReader(bad)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("远程地区缺令牌应 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// 更新:留空 token 保留原值。
	upd, _ := json.Marshal(map[string]any{
		"name": "西南2", "baseDomain": "cd.easylan.site", "regionCode": "cd",
		"agentUrl": "https://agent2.cd.easylan.site", "maxContainers": 80,
	})
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/regions/cd", bytes.NewReader(upd))
	req.SetPathValue("id", "cd")
	s.handleAdminUpdateRegion(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新地区失败 %d: %s", rec.Code, rec.Body.String())
	}
	reg, _ = s.store.GetRegion(context.Background(), "cd")
	if reg.Name != "西南2" || reg.AgentToken != "tok" || reg.AgentURL != "https://agent2.cd.easylan.site" {
		t.Fatalf("更新结果错误: %+v", reg)
	}

	// 本地地区不允许代理地址。
	badLocal, _ := json.Marshal(map[string]any{"name": "华中", "baseDomain": "wh.easylan.site", "regionCode": "wh", "agentUrl": "https://x"})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/admin/regions/wh", bytes.NewReader(badLocal))
	req.SetPathValue("id", "wh")
	s.handleAdminUpdateRegion(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("本地地区配代理地址应 400, got %d", rec.Code)
	}

	// 有实例后禁止改基础域名。
	inst := &store.Instance{ID: "derp-a", Name: "derp-a", Subdomain: "derp-a.relay.cd.easylan.site",
		Region: "cd", RegionCode: "cd", RegionID: 930, STUNPort: 3500, ContainerName: "derp-a", ExpiresAt: time.Now().Add(time.Hour), State: "active", CreatedAt: time.Now()}
	if err := s.store.CreateInstance(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	badDomain, _ := json.Marshal(map[string]any{"name": "西南2", "baseDomain": "cd2.easylan.site", "regionCode": "cd",
		"agentUrl": "https://agent2.cd.easylan.site", "maxContainers": 80})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/admin/regions/cd", bytes.NewReader(badDomain))
	req.SetPathValue("id", "cd")
	s.handleAdminUpdateRegion(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("有实例时改域名应 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// 有实例删地区 → 409。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/v1/admin/regions/cd", nil)
	req.SetPathValue("id", "cd")
	s.handleAdminDeleteRegion(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("有实例删地区应 409, got %d", rec.Code)
	}

	// 本地地区不可删除。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/v1/admin/regions/wh", nil)
	req.SetPathValue("id", "wh")
	s.handleAdminDeleteRegion(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("删本地地区应 400, got %d", rec.Code)
	}
}

func TestAdminRegions_ListShowsInstanceCount(t *testing.T) {
	s := newAdminServer(t)
	seedRegion(t, s, &store.Region{ID: "wh", Name: "华中", BaseDomain: "wh.easylan.site", RegionCode: "wh", Enabled: true})
	for i := 0; i < 2; i++ {
		inst := &store.Instance{
			ID: "derp-wh-" + string(rune('a'+i)), Name: "derp-wh-" + string(rune('a'+i)),
			Subdomain: "derp-wh-" + string(rune('a'+i)) + ".relay.wh.easylan.site",
			Region:    "wh", RegionCode: "wh", RegionID: 900 + i, STUNPort: 3478 + i,
			ContainerName: "derp-wh-" + string(rune('a'+i)),
			ExpiresAt:     time.Now().Add(time.Hour), State: "active", CreatedAt: time.Now(),
		}
		if err := s.store.CreateInstance(context.Background(), inst); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	s.handleAdminRegions(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/regions", nil))
	var env struct {
		Data struct {
			Regions []adminRegionView `json:"regions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Regions) != 1 || env.Data.Regions[0].InstanceCount != 2 {
		t.Fatalf("实例数统计错误: %+v", env.Data.Regions)
	}
	if !env.Data.Regions[0].Local {
		t.Error("wh 应为本地地区")
	}
}

// TestRedeem_RemoteRegionViaAgent 端到端:兑换选 cd → 控制面路由到 regiond 代理建容器。
func TestRedeem_RemoteRegionViaAgent(t *testing.T) {
	s := newAdminServer(t)
	ctx := context.Background()

	// 本地地区 wh。
	seedRegion(t, s, &store.Region{ID: "wh", Name: "华中", BaseDomain: "wh.easylan.site", RegionCode: "wh", Enabled: true})

	// 远程地区 cd:起一个假 regiond。
	remote := &fakeOrch{}
	remoteSrv := httptest.NewServer(agent.NewServer(remote, "tok").Handler())
	defer remoteSrv.Close()
	seedRegion(t, s, &store.Region{
		ID: "cd", Name: "西南", BaseDomain: "cd.easylan.site", RegionCode: "cd",
		AgentURL: remoteSrv.URL, AgentToken: "tok", Enabled: true,
	})

	// 控制面编排路由:本地走 fakeOrch,远程走 agent。
	local := &fakeOrch{}
	router := region.New(s.store, local, "wh")
	s.orch = router
	s.limits = limits.New(limits.Options{MaxContainers: 100, MemoryWatermarkPct: 80, CPUWatermarkPct: 80}, router, s.store)

	if _, err := s.store.CreateRedeemCodes(ctx, []store.RedeemCode{
		{Code: "DERP-REMOTE-0001", DurationValue: 30, DurationUnit: "day", CreatedBy: "admin", CreatedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": "DERP-REMOTE-0001", "region": "cd"})
	rec := httptest.NewRecorder()
	s.rateLimit(s.handleRedeem)(rec, httptest.NewRequest(http.MethodPost, "/v1/redeem/redeem", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("兑换失败 %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Instance store.Instance `json:"instance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Instance.Region != "cd" {
		t.Errorf("实例地区应为 cd, got %q", env.Data.Instance.Region)
	}
	if len(remote.ensured) != 1 {
		t.Fatalf("regiond 应收到 1 次建容器, got %d", len(remote.ensured))
	}
	if remote.ensured[0].ContainerName != env.Data.Instance.Name {
		t.Errorf("容器名不一致: %q vs %q", remote.ensured[0].ContainerName, env.Data.Instance.Name)
	}
	got, err := s.store.GetInstance(ctx, env.Data.Instance.ID)
	if err != nil || got.Region != "cd" {
		t.Fatalf("实例未落库或地区错误: %+v err=%v", got, err)
	}
}
