package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"derp-orch/internal/config"
	"derp-orch/internal/docker"
	"derp-orch/internal/lifecycle"
	"derp-orch/internal/store"
)

// fakeOrch 可编程假编排器,验证调用与补偿逻辑。
type fakeOrch struct {
	ensureErr error
	removed   []string
}

func (f *fakeOrch) EnsureNode(ctx context.Context, i *store.Instance) error { return f.ensureErr }
func (f *fakeOrch) RemoveNode(ctx context.Context, i *store.Instance) error {
	f.removed = append(f.removed, i.ID)
	return nil
}
func (f *fakeOrch) NodeState(ctx context.Context, i *store.Instance) (docker.ContainerState, error) {
	return docker.StateRunning, nil
}

func newTestServer(t *testing.T, orch docker.Orchestrator) *Server {
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
		AdminAPIKey:     "test-key",
	}
	sweep := lifecycle.New(st, orch, time.Hour)
	return &Server{cfg: cfg, store: st, orch: orch, sweep: sweep}
}

func TestCreateInstance_TTLValidation(t *testing.T) {
	s := newTestServer(t, &fakeOrch{})
	cases := []struct {
		ttl  int
		code int
	}{
		{0, http.StatusCreated},
		{1, http.StatusCreated},
		{168, http.StatusCreated},
		{169, http.StatusBadRequest},
		{-1, http.StatusBadRequest},
	}
	for _, c := range cases {
		body, _ := json.Marshal(map[string]int{"ttlHours": c.ttl})
		req := httptest.NewRequest(http.MethodPost, "/v1/instances", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleCreateInstance(rec, req)
		if rec.Code != c.code {
			t.Errorf("ttl=%d 期望 %d,实际 %d", c.ttl, c.code, rec.Code)
		}
	}
}

func TestCreateInstance_CompensationOnDockerFailure(t *testing.T) {
	orch := &fakeOrch{ensureErr: errors.New("docker down")}
	s := newTestServer(t, orch)
	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/instances", body)
	rec := httptest.NewRecorder()
	s.handleCreateInstance(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("容器失败应 500,实际 %d", rec.Code)
	}
	// 补偿: 无残留实例行。
	list, err := s.store.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("容器失败后应无残留实例,实际 %+v", list)
	}
}

func TestCreateInstance_SuccessReturnsDerpMap(t *testing.T) {
	s := newTestServer(t, &fakeOrch{})
	body := bytes.NewBufferString(`{"ttlHours": 6}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/instances", body)
	rec := httptest.NewRecorder()
	s.handleCreateInstance(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("期望 201,实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Instance *store.Instance `json:"instance"`
			DerpMap  json.RawMessage `json:"derpMap"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	inst := resp.Data.Instance
	if inst.Subdomain != inst.Name+".relay.wh.easylan.site" {
		t.Errorf("子域名派生错误: %s (name=%s)", inst.Subdomain, inst.Name)
	}
	if len(resp.Data.DerpMap) == 0 {
		t.Error("derpMap 缺失")
	}
	// 有效期 6 小时。
	if got := inst.ExpiresAt.Sub(inst.CreatedAt); got != 6*time.Hour {
		t.Errorf("有效期应为 6h,实际 %s", got)
	}
	// regionID/stunPort 已分配。
	if inst.RegionID != 900 || inst.STUNPort != 3478 {
		t.Errorf("资源分配错误: region=%d stun=%d", inst.RegionID, inst.STUNPort)
	}
}

func TestDeleteInstance_Idempotent(t *testing.T) {
	orch := &fakeOrch{}
	s := newTestServer(t, orch)
	// PathValue 依赖路由匹配,经 mux 分发。
	mux := http.NewServeMux()
	mux.Handle("POST /v1/instances", http.HandlerFunc(s.handleCreateInstance))
	mux.Handle("DELETE /v1/instances/{id}", http.HandlerFunc(s.handleDeleteInstance))

	// 不存在 → ok。
	req := httptest.NewRequest(http.MethodDelete, "/v1/instances/nope", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("删除不存在实例应幂等 ok,实际 %d", rec.Code)
	}

	// 建实例再删。
	body := bytes.NewBufferString(`{}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/instances", body)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Instance *store.Instance `json:"instance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("创建响应解析失败: %v", err)
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/instances/"+resp.Data.Instance.ID, nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除失败: %d", rec.Code)
	}
	if len(orch.removed) != 1 || orch.removed[0] != resp.Data.Instance.ID {
		t.Errorf("编排器应收到删除调用,实际 %v", orch.removed)
	}
	// 行已清。
	if _, err := s.store.GetInstance(context.Background(), resp.Data.Instance.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("删除后实例应不存在,实际 %v", err)
	}
}

func TestRegionCodeFromName(t *testing.T) {
	if got := regionCodeFromName("derp-20260812-x7k2"); got != "260812" {
		t.Errorf("regionCode 应为 260812,实际 %q", got)
	}
	if got := regionCodeFromName("short"); got != "derp" {
		t.Errorf("短名回退应为 derp,实际 %q", got)
	}
}
