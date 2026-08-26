package limits

import (
	"context"
	"testing"
	"time"

	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// fakeOrch 返回可编程 stats。
type fakeOrch struct {
	stats map[string]*docker.ContainerStat
}

func (f *fakeOrch) EnsureNode(ctx context.Context, i *store.Instance) error { return nil }
func (f *fakeOrch) RemoveNode(ctx context.Context, i *store.Instance) error { return nil }
func (f *fakeOrch) NodeState(ctx context.Context, i *store.Instance) (docker.ContainerState, error) {
	return docker.StateRunning, nil
}
func (f *fakeOrch) Stats(ctx context.Context) (map[string]*docker.ContainerStat, error) {
	return f.stats, nil
}

func mustStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedActive(t *testing.T, st *store.Store, n int) {
	t.Helper()
	base := time.Now()
	for i := 0; i < n; i++ {
		inst := &store.Instance{
			ID:            "derp-20260826-000" + string(rune('a'+i)),
			Name:          "derp-20260826-000" + string(rune('a'+i)),
			Subdomain:     "derp-20260826-000" + string(rune('a'+i)) + ".relay.wh.easylan.site",
			RegionCode:    "260826",
			ContainerName: "derp-20260826-000" + string(rune('a'+i)),
			ExpiresAt:     base.Add(time.Hour),
			State:         "active",
			CreatedAt:     base,
		}
		if err := st.CreateInstanceAllocated(context.Background(), inst, 900, 3478); err != nil {
			t.Fatalf("CreateInstanceAllocated: %v", err)
		}
	}
}

func TestCanCreate_MaxContainers(t *testing.T) {
	st := mustStore(t)
	seedActive(t, st, 2)
	orch := &fakeOrch{stats: map[string]*docker.ContainerStat{}}
	c := New(Options{MaxContainers: 2, MemoryWatermarkPct: 80, CPUWatermarkPct: 80}, orch, st)

	ok, reason, _, err := c.CanCreate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("应拒绝(已达上限 2), reason=%q", reason)
	}
}

func TestCanCreate_CPUWatermark(t *testing.T) {
	st := mustStore(t)
	orch := &fakeOrch{stats: map[string]*docker.ContainerStat{
		"c1": {CPUPct: 90, MemBytes: 100},
	}}
	c := New(Options{MaxContainers: 10, MemoryWatermarkPct: 80, CPUWatermarkPct: 80}, orch, st)
	ok, reason, _, err := c.CanCreate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("CPU 90%% 超水位应拒绝, reason=%q", reason)
	}
}

func TestCanCreate_Allow(t *testing.T) {
	st := mustStore(t)
	orch := &fakeOrch{stats: map[string]*docker.ContainerStat{
		"c1": {CPUPct: 10, MemBytes: 100},
	}}
	c := New(Options{MaxContainers: 10, MemoryWatermarkPct: 80, CPUWatermarkPct: 80}, orch, st)
	ok, _, _, err := c.CanCreate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("低负载应允许")
	}
}
