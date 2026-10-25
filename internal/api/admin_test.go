package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"derp-orch/internal/auth"
	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// newAdminTestServer 在 newTestServer 基础上注入 admin 认证管理器。
// Verify 永远通过 → 任意密码即可登录,简化测试。
func newAdminTestServer(t *testing.T, orch docker.Orchestrator) *Server {
	t.Helper()
	s := newTestServer(t, orch)
	s.authmgr = auth.New(auth.Options{
		Username:   "admin",
		SessionTTL: time.Hour,
		Verify:     func(string) bool { return true },
	})
	return s
}

// loginAsAdmin 走 authmgr.Login 拿 session token,模拟已登录态。
func loginAsAdmin(t *testing.T, s *Server) string {
	t.Helper()
	tok, err := s.authmgr.Login("admin", "ok")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return tok
}

// seedInstance 直接通过 store 插一条带分配的实例行,跳过 orchestrator 创建。
func seedInstance(t *testing.T, s *Server, id string) *store.Instance {
	t.Helper()
	inst := &store.Instance{
		ID:            id,
		Name:          id,
		Subdomain:     id + ".relay.wh.easylan.site",
		RegionCode:    "test",
		ContainerName: id,
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		State:         "active",
		CreatedAt:     time.Now(),
	}
	if err := s.store.CreateInstanceAllocated(context.Background(), inst, s.cfg.RegionIDBase, s.cfg.RegionIDMax, s.cfg.STUNPortBase); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return inst
}

func TestAdminDeleteInstance_Success(t *testing.T) {
	orch := &fakeOrch{}
	s := newAdminTestServer(t, orch)
	inst := seedInstance(t, s, "derp-test-001")
	tok := loginAsAdmin(t, s)

	mux := http.NewServeMux()
	mux.Handle("DELETE /v1/admin/instances/{id}", http.HandlerFunc(s.handleAdminDeleteInstance))
	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/instances/"+inst.ID, nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: tok})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200,实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Deleted bool `json:"deleted"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if !resp.OK || !resp.Data.Deleted {
		t.Errorf("期望 deleted=true,实际 %+v", resp)
	}
	// 编排器收到 RemoveNode 调用。
	if len(orch.removed) != 1 || orch.removed[0] != inst.ID {
		t.Errorf("RemoveNode 未调用或 id 不匹配: %v", orch.removed)
	}
	// store 行已清。
	if _, err := s.store.GetInstance(context.Background(), inst.ID); err == nil {
		t.Error("删除后实例应不存在")
	}
	// 审计 1 条。
	logs, err := s.store.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("审计条数应为 1,实际 %d", len(logs))
	}
	if logs[0].Kind != "admin_delete_instance" {
		t.Errorf("审计 Kind 应为 admin_delete_instance,实际 %s", logs[0].Kind)
	}
	if logs[0].Username != "admin" {
		t.Errorf("审计 Username 应为 admin,实际 %s", logs[0].Username)
	}
	if logs[0].InstanceID != inst.ID {
		t.Errorf("审计 InstanceID 应为 %s,实际 %s", inst.ID, logs[0].InstanceID)
	}
}

func TestAdminDeleteInstance_Idempotent(t *testing.T) {
	orch := &fakeOrch{}
	s := newAdminTestServer(t, orch)
	tok := loginAsAdmin(t, s)

	mux := http.NewServeMux()
	mux.Handle("DELETE /v1/admin/instances/{id}", http.HandlerFunc(s.handleAdminDeleteInstance))
	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/instances/nope", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: tok})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200,实际 %d", rec.Code)
	}
	var resp struct {
		Data struct {
			Deleted bool `json:"deleted"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.Deleted {
		t.Error("不存在实例应返回 deleted=false")
	}
	if len(orch.removed) != 0 {
		t.Errorf("RemoveNode 不应被调用,实际 %v", orch.removed)
	}
	logs, _ := s.store.ListAudit(context.Background(), 10)
	if len(logs) != 0 {
		t.Errorf("幂等删除不应写审计,实际 %d 条", len(logs))
	}
}

func TestAdminDerpMap_Success(t *testing.T) {
	orch := &fakeOrch{}
	s := newAdminTestServer(t, orch)
	s.cfg.DERPPort = 443
	inst := seedInstance(t, s, "derp-test-002")
	tok := loginAsAdmin(t, s)

	mux := http.NewServeMux()
	mux.Handle("GET /v1/admin/instances/{id}/derpmap", http.HandlerFunc(s.handleAdminDerpMap))
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/instances/"+inst.ID+"/derpmap", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: tok})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200,实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			DerpMap json.RawMessage `json:"derpMap"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(resp.Data.DerpMap) == 0 {
		t.Fatal("derpMap 字段缺失")
	}
	// 解 derpMap,验证 nodes[0].hostName == subdomain。
	var m struct {
		Regions map[string]struct {
			Nodes []struct {
				HostName string `json:"hostName"`
			} `json:"nodes"`
		} `json:"regions"`
	}
	if err := json.Unmarshal(resp.Data.DerpMap, &m); err != nil {
		t.Fatalf("derpMap 解析失败: %v", err)
	}
	if len(m.Regions) == 0 {
		t.Fatal("regions 应非空")
	}
	for _, r := range m.Regions {
		if len(r.Nodes) == 0 {
			t.Fatal("nodes 应非空")
		}
		if r.Nodes[0].HostName != inst.Subdomain {
			t.Errorf("hostName=%q,期望 %q", r.Nodes[0].HostName, inst.Subdomain)
		}
	}
}

func TestAdminDerpMap_NotFound(t *testing.T) {
	orch := &fakeOrch{}
	s := newAdminTestServer(t, orch)
	tok := loginAsAdmin(t, s)

	mux := http.NewServeMux()
	mux.Handle("GET /v1/admin/instances/{id}/derpmap", http.HandlerFunc(s.handleAdminDerpMap))
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/instances/missing", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: tok})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("期望 404,实际 %d: %s", rec.Code, rec.Body.String())
	}
}
