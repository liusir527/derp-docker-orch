package lifecycle

import (
	"context"
	"testing"
	"time"

	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// fakeOrch 记录删除调用。
type fakeOrch struct {
	removed []string
}

func (f *fakeOrch) EnsureNode(ctx context.Context, i *store.Instance) error { return nil }
func (f *fakeOrch) RemoveNode(ctx context.Context, i *store.Instance) error {
	f.removed = append(f.removed, i.ID)
	return nil
}
func (f *fakeOrch) NodeState(ctx context.Context, i *store.Instance) (docker.ContainerState, error) {
	return docker.StateRunning, nil
}
func (f *fakeOrch) Stats(ctx context.Context) (map[string]*docker.ContainerStat, error) {
	return map[string]*docker.ContainerStat{}, nil
}

func TestSweepOnce_RemovesExpired(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	now := time.Now().UTC()
	// 过期实例。
	expired := &store.Instance{
		ID: "derp-20260701-aaaa", Name: "derp-20260701-aaaa",
		Subdomain: "e.relay.test", RegionID: 900, RegionCode: "260701",
		STUNPort: 3478, ContainerName: "derp-20260701-aaaa",
		ExpiresAt: now.Add(-time.Hour), State: "active", CreatedAt: now,
	}
	if err := st.CreateInstance(ctx, expired); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	// 未过期实例。
	live := &store.Instance{
		ID: "derp-20260812-aaaa", Name: "derp-20260812-aaaa",
		Subdomain: "l.relay.test", RegionID: 901, RegionCode: "260812",
		STUNPort: 3479, ContainerName: "derp-20260812-aaaa",
		ExpiresAt: now.Add(time.Hour), State: "active", CreatedAt: now,
	}
	if err := st.CreateInstance(ctx, live); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	orch := &fakeOrch{}
	sw := New(st, orch, time.Hour)
	sw.SweepOnce(ctx)

	if len(orch.removed) != 1 || orch.removed[0] != "derp-20260701-aaaa" {
		t.Errorf("应只删除过期实例,实际 %v", orch.removed)
	}
	// 过期行已清,活跃行保留。
	if _, err := st.GetInstance(ctx, "derp-20260701-aaaa"); err != store.ErrNotFound {
		t.Errorf("过期实例应已删除,实际 %v", err)
	}
	if _, err := st.GetInstance(ctx, "derp-20260812-aaaa"); err != nil {
		t.Errorf("活跃实例应保留: %v", err)
	}
}

func TestSweepOnce_NoExpiredNoOp(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	orch := &fakeOrch{}
	sw := New(st, orch, time.Hour)
	sw.SweepOnce(context.Background())

	if len(orch.removed) != 0 {
		t.Errorf("无过期实例不应触发删除,实际 %v", orch.removed)
	}
	if sw.LastSweepAt.IsZero() {
		t.Error("LastSweepAt 应被更新")
	}
}
