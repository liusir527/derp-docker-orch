package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

type fakeOrch struct {
	ensured []*store.Instance
	removed []string
	err     error
	stats   map[string]*docker.ContainerStat
}

func (f *fakeOrch) EnsureNode(ctx context.Context, i *store.Instance) error {
	f.ensured = append(f.ensured, i)
	return f.err
}
func (f *fakeOrch) RemoveNode(ctx context.Context, i *store.Instance) error {
	f.removed = append(f.removed, i.ContainerName)
	return f.err
}
func (f *fakeOrch) NodeState(ctx context.Context, i *store.Instance) (docker.ContainerState, error) {
	return docker.StateRunning, f.err
}
func (f *fakeOrch) Stats(ctx context.Context) (map[string]*docker.ContainerStat, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.stats == nil {
		return map[string]*docker.ContainerStat{}, nil
	}
	return f.stats, nil
}
func (f *fakeOrch) HostInfo(ctx context.Context) (docker.HostInfo, error) {
	return docker.HostInfo{MemoryTotalBytes: 4 << 30, CPUCores: 2}, nil
}
func (f *fakeOrch) Ping(ctx context.Context) error { return f.err }

func TestServer_Auth(t *testing.T) {
	orch := &fakeOrch{}
	srv := httptest.NewServer(NewServer(orch, "secret").Handler())
	defer srv.Close()

	// 无令牌 → 401。
	res, err := http.Get(srv.URL + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401, got %d", res.StatusCode)
	}

	// healthz 公开。
	res, err = http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("healthz 应 200, got %d", res.StatusCode)
	}

	// 空令牌配置 → 503。
	empty := httptest.NewServer(NewServer(orch, "").Handler())
	defer empty.Close()
	res, err = http.Get(empty.URL + "/healthz")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("healthz 应 200")
	}
	req, _ := http.NewRequest(http.MethodGet, empty.URL+"/v1/stats", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未配置令牌应 503, got %d", res.StatusCode)
	}
}

func TestClientServer_RoundTrip(t *testing.T) {
	orch := &fakeOrch{stats: map[string]*docker.ContainerStat{
		"derp-x": {Container: "derp-x", CPUPct: 5, MemBytes: 100 << 20},
	}}
	srv := httptest.NewServer(NewServer(orch, "tok").Handler())
	defer srv.Close()
	c := NewClient(srv.URL, "tok")
	ctx := context.Background()

	inst := &store.Instance{ID: "derp-x", ContainerName: "derp-x", Subdomain: "derp-x.relay.cd.easylan.site", Region: "cd"}
	if err := c.EnsureNode(ctx, inst); err != nil {
		t.Fatalf("EnsureNode: %v", err)
	}
	if len(orch.ensured) != 1 || orch.ensured[0].ContainerName != "derp-x" {
		t.Fatalf("服务端未收到创建请求: %+v", orch.ensured)
	}
	state, err := c.NodeState(ctx, inst)
	if err != nil || state != docker.StateRunning {
		t.Fatalf("NodeState: %v %v", state, err)
	}
	if err := c.RemoveNode(ctx, inst); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
	if len(orch.removed) != 1 || orch.removed[0] != "derp-x" {
		t.Fatalf("服务端未收到删除请求: %+v", orch.removed)
	}
	stats, err := c.Stats(ctx)
	if err != nil || stats["derp-x"] == nil {
		t.Fatalf("Stats: %+v err=%v", stats, err)
	}
	info, err := c.Info(ctx)
	if err != nil || !info.DockerOK || info.MemoryTotalBytes != 4<<30 {
		t.Fatalf("Info: %+v err=%v", info, err)
	}
}

func TestClient_ErrorPropagation(t *testing.T) {
	orch := &fakeOrch{err: errors.New("docker 挂了")}
	srv := httptest.NewServer(NewServer(orch, "tok").Handler())
	defer srv.Close()

	c := NewClient(srv.URL, "wrong")
	_, err := c.Info(context.Background())
	if err == nil {
		t.Fatal("错误令牌应报错")
	}

	c = NewClient(srv.URL, "tok")
	err = c.EnsureNode(context.Background(), &store.Instance{ID: "a", ContainerName: "a", Subdomain: "a.test"})
	if err == nil {
		t.Fatal("服务端错误应回传")
	}
}
