package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newInstance 构造一个待插入实例。
func newInstance(id string) *Instance {
	now := time.Now().UTC()
	return &Instance{
		ID: id, Name: id, Subdomain: id + ".relay.test",
		RegionCode: "test", ContainerName: id,
		ExpiresAt: now.Add(time.Hour), State: "active", CreatedAt: now,
	}
}

func insertAllocated(t *testing.T, s *Store, id string, regionBase, stunBase int) *Instance {
	t.Helper()
	inst := newInstance(id)
	if err := s.CreateInstanceAllocated(context.Background(), inst, regionBase, 0, stunBase); err != nil {
		t.Fatalf("CreateInstanceAllocated(%s): %v", id, err)
	}
	return inst
}

func TestCreateInstanceAllocated_Concurrent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	var mu sync.Mutex
	regions := map[int]bool{}
	stuns := map[int]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			inst := newInstance(fmt.Sprintf("inst-%d-%d", n, time.Now().UnixNano()))
			if err := s.CreateInstanceAllocated(ctx, inst, 900, 0, 3478); err != nil {
				t.Errorf("CreateInstanceAllocated: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if regions[inst.RegionID] {
				t.Errorf("重复 regionID: %d", inst.RegionID)
			}
			if stuns[inst.STUNPort] {
				t.Errorf("重复 stunPort: %d", inst.STUNPort)
			}
			regions[inst.RegionID] = true
			stuns[inst.STUNPort] = true
		}(i)
	}
	wg.Wait()
	if len(regions) != 5 {
		t.Errorf("应分配 5 个 regionID,实际 %d", len(regions))
	}
	if len(stuns) != 5 {
		t.Errorf("应分配 5 个 stunPort,实际 %d", len(stuns))
	}
}

func TestCreateInstanceAllocated_ReuseAfterDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := insertAllocated(t, s, "inst-a", 900, 3478)
	b := insertAllocated(t, s, "inst-b", 900, 3478)
	if a.STUNPort == b.STUNPort {
		t.Fatalf("端口不应重复分配: %d", a.STUNPort)
	}

	if err := s.DeleteInstance(ctx, "inst-a"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	// 删除后端口复用。
	c := insertAllocated(t, s, "inst-c", 900, 3478)
	if c.STUNPort != a.STUNPort {
		t.Errorf("删除后应复用端口 %d,实际 %d", a.STUNPort, c.STUNPort)
	}
	if c.RegionID != a.RegionID {
		t.Errorf("删除后应复用 regionID %d,实际 %d", a.RegionID, c.RegionID)
	}
}

func TestCreateInstanceAllocated_GapFilling(t *testing.T) {
	s := newTestStore(t)
	insertAllocated(t, s, "inst-a", 900, 3478) // region 900
	insertAllocated(t, s, "inst-b", 900, 3478) // region 901
	// 删除 901,应补空档。
	if err := s.DeleteInstance(context.Background(), "inst-b"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	c := insertAllocated(t, s, "inst-c", 900, 3478)
	if c.RegionID != 901 {
		t.Errorf("应补空档 901,实际 %d", c.RegionID)
	}
}

func TestListExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 已过期实例。
	now := time.Now().UTC()
	expired := newInstance("expired-1")
	expired.ExpiresAt = now.Add(-time.Minute)
	if err := s.CreateInstanceAllocated(ctx, expired, 950, 0, 3950); err != nil {
		t.Fatalf("CreateInstanceAllocated: %v", err)
	}
	insertAllocated(t, s, "live-1", 951, 3951)

	exp, err := s.ListExpired(ctx, now)
	if err != nil {
		t.Fatalf("ListExpired: %v", err)
	}
	if len(exp) != 1 || exp[0].ID != "expired-1" {
		t.Errorf("应只返回过期实例,实际 %+v", exp)
	}
}

func TestGetInstance_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetInstance(context.Background(), "nope"); err != ErrNotFound {
		t.Errorf("应返回 ErrNotFound,实际 %v", err)
	}
}
