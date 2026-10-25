package region

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"derp-orch/internal/agent"
	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

type fakeOrch struct {
	ensured  []string
	removed  []string
	stats    map[string]*docker.ContainerStat
	hostInfo docker.HostInfo
}

func (f *fakeOrch) EnsureNode(ctx context.Context, i *store.Instance) error {
	f.ensured = append(f.ensured, i.Region+"/"+i.ContainerName)
	return nil
}

func (f *fakeOrch) RemoveNode(ctx context.Context, i *store.Instance) error {
	f.removed = append(f.removed, i.ContainerName)
	return nil
}

func (f *fakeOrch) NodeState(ctx context.Context, i *store.Instance) (docker.ContainerState, error) {
	return docker.StateRunning, nil
}

func (f *fakeOrch) Stats(ctx context.Context) (map[string]*docker.ContainerStat, error) {
	if f.stats == nil {
		return map[string]*docker.ContainerStat{}, nil
	}
	return f.stats, nil
}

func (f *fakeOrch) HostInfo(ctx context.Context) (docker.HostInfo, error) { return f.hostInfo, nil }

func (f *fakeOrch) Ping(ctx context.Context) error { return nil }

func newRouterStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestRouter_LocalAndRemote(t *testing.T) {
	st := newRouterStore(t)
	ctx := context.Background()
	if err := st.CreateRegion(ctx, &store.Region{ID: "wh", Name: "华中", BaseDomain: "wh.easylan.site", RegionCode: "wh", Enabled: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	local := &fakeOrch{hostInfo: docker.HostInfo{MemoryTotalBytes: 8 << 30, CPUCores: 4}}
	remote := &fakeOrch{}
	remoteSrv := httptest.NewServer(agent.NewServer(remote, "tok").Handler())
	defer remoteSrv.Close()
	if err := st.CreateRegion(ctx, &store.Region{
		ID: "cd", Name: "西南", BaseDomain: "cd.easylan.site", RegionCode: "cd",
		AgentURL: remoteSrv.URL, AgentToken: "tok", Enabled: true, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	r := New(st, local, "wh")

	// 本地地区。
	localInst := &store.Instance{ID: "a", ContainerName: "derp-a", Region: "wh"}
	if err := r.EnsureNode(ctx, localInst); err != nil {
		t.Fatalf("本地 EnsureNode: %v", err)
	}
	if len(local.ensured) != 1 || len(remote.ensured) != 0 {
		t.Fatalf("本地调用错位: local=%v remote=%v", local.ensured, remote.ensured)
	}

	// 空 region 走本地。
	emptyInst := &store.Instance{ID: "b", ContainerName: "derp-b", Region: ""}
	if err := r.EnsureNode(ctx, emptyInst); err != nil {
		t.Fatalf("空地区 EnsureNode: %v", err)
	}
	if len(local.ensured) != 2 {
		t.Fatalf("空地区应走本地: %v", local.ensured)
	}

	// 远程地区。
	remoteInst := &store.Instance{ID: "c", ContainerName: "derp-c", Subdomain: "derp-c.relay.cd.easylan.site", Region: "cd"}
	if err := r.EnsureNode(ctx, remoteInst); err != nil {
		t.Fatalf("远程 EnsureNode: %v", err)
	}
	if len(remote.ensured) != 1 || remote.ensured[0] != "cd/derp-c" {
		t.Fatalf("远程调用错位: %v", remote.ensured)
	}

	// 删除按地区分发。
	if err := r.RemoveNode(ctx, remoteInst); err != nil {
		t.Fatalf("远程 RemoveNode: %v", err)
	}
	if len(remote.removed) != 1 || len(local.removed) != 0 {
		t.Fatalf("远程删除错位: local=%v remote=%v", local.removed, remote.removed)
	}

	// 未知地区报错。
	if err := r.EnsureNode(ctx, &store.Instance{ID: "x", ContainerName: "derp-x", Region: "nope"}); err == nil {
		t.Fatal("未知地区应报错")
	}
}

func TestRouter_StatsAndHostInfo(t *testing.T) {
	st := newRouterStore(t)
	ctx := context.Background()
	if err := st.CreateRegion(ctx, &store.Region{ID: "wh", Name: "华中", BaseDomain: "wh.easylan.site", RegionCode: "wh", Enabled: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	local := &fakeOrch{
		stats:    map[string]*docker.ContainerStat{"derp-a": {Container: "derp-a", CPUPct: 10, MemBytes: 1 << 30}},
		hostInfo: docker.HostInfo{MemoryTotalBytes: 4 << 30, CPUCores: 2},
	}
	remote := &fakeOrch{
		stats:    map[string]*docker.ContainerStat{"derp-b": {Container: "derp-b", CPUPct: 20, MemBytes: 2 << 30}},
		hostInfo: docker.HostInfo{MemoryTotalBytes: 8 << 30, CPUCores: 8},
	}
	remoteSrv := httptest.NewServer(agent.NewServer(remote, "tok").Handler())
	defer remoteSrv.Close()
	if err := st.CreateRegion(ctx, &store.Region{
		ID: "cd", Name: "西南", BaseDomain: "cd.easylan.site", RegionCode: "cd",
		AgentURL: remoteSrv.URL, AgentToken: "tok", Enabled: true, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	r := New(st, local, "wh")
	all, err := r.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(all) != 2 || all["derp-a"] == nil || all["derp-b"] == nil {
		t.Fatalf("应合并两地区容器统计, got %+v", all)
	}
	whStats, err := r.StatsFor(ctx, "wh")
	if err != nil || len(whStats) != 1 {
		t.Fatalf("StatsFor(wh): %v %+v", err, whStats)
	}
	info, err := r.HostInfoFor(ctx, "cd")
	if err != nil || info.MemoryTotalBytes != 8<<30 {
		t.Fatalf("远程 HostInfoFor: %+v err=%v", info, err)
	}
	info, err = r.HostInfoFor(ctx, "wh")
	if err != nil || info.MemoryTotalBytes != 4<<30 {
		t.Fatalf("本地 HostInfoFor: %+v err=%v", info, err)
	}
	probe, err := r.AgentInfo(ctx, "cd")
	if err != nil || !probe.DockerOK || probe.MemoryTotalBytes != 8<<30 {
		t.Fatalf("远程 AgentInfo: %+v err=%v", probe, err)
	}
}
