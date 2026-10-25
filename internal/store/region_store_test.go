package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegion_CRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	reg := &Region{
		ID: "cd", Name: "西南", BaseDomain: "cd.easylan.site", RegionCode: "cd",
		AgentURL: "https://agent.cd.easylan.site", AgentToken: "tok", MaxContainers: 50,
		Enabled: true, CreatedAt: time.Now(),
	}
	if err := s.CreateRegion(ctx, reg); err != nil {
		t.Fatalf("CreateRegion: %v", err)
	}
	// 重复 id 报唯一冲突。
	if err := s.CreateRegion(ctx, reg); err == nil {
		t.Fatal("重复地区应报错")
	}
	got, err := s.GetRegion(ctx, "cd")
	if err != nil {
		t.Fatalf("GetRegion: %v", err)
	}
	if got.Name != "西南" || !got.Enabled || got.MaxContainers != 50 || got.AgentToken != "tok" {
		t.Fatalf("读取结果错误: %+v", got)
	}
	if got.Local() {
		t.Error("配置了代理地址的地区不应是本地地区")
	}

	// 更新。
	got.Name = "西南2"
	got.Enabled = false
	if err := s.UpdateRegion(ctx, got); err != nil {
		t.Fatalf("UpdateRegion: %v", err)
	}
	got2, _ := s.GetRegion(ctx, "cd")
	if got2.Name != "西南2" || got2.Enabled {
		t.Fatalf("更新未生效: %+v", got2)
	}
	// 更新不存在的地区 → ErrRegionNotFound。
	missing := &Region{ID: "nope", Name: "x", BaseDomain: "x.test", RegionCode: "x", CreatedAt: time.Now()}
	if err := s.UpdateRegion(ctx, missing); !errors.Is(err, ErrRegionNotFound) {
		t.Fatalf("更新不存在地区应 ErrRegionNotFound, got %v", err)
	}

	// 列表按创建时间升序。
	wh := &Region{ID: "wh", Name: "华中", BaseDomain: "wh.easylan.site", RegionCode: "wh", Enabled: true, CreatedAt: time.Now().Add(-time.Hour)}
	if err := s.CreateRegion(ctx, wh); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListRegions(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListRegions: %v len=%d", err, len(list))
	}
	if list[0].ID != "wh" {
		t.Errorf("应按创建时间升序, got %s first", list[0].ID)
	}

	// 删除。
	if err := s.DeleteRegion(ctx, "cd"); err != nil {
		t.Fatalf("DeleteRegion: %v", err)
	}
	if _, err := s.GetRegion(ctx, "cd"); !errors.Is(err, ErrRegionNotFound) {
		t.Fatalf("删除后应 ErrRegionNotFound, got %v", err)
	}
	if err := s.DeleteRegion(ctx, "cd"); !errors.Is(err, ErrRegionNotFound) {
		t.Fatalf("重复删除应 ErrRegionNotFound, got %v", err)
	}
}

func TestDeleteRegion_InUse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateRegion(ctx, &Region{ID: "cd", Name: "西南", BaseDomain: "cd.test", RegionCode: "cd", Enabled: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	inst := newInstance("derp-inuse-1")
	inst.Region = "cd"
	if err := s.CreateInstanceAllocated(ctx, inst, 900, 0, 3478); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRegion(ctx, "cd"); !errors.Is(err, ErrRegionInUse) {
		t.Fatalf("有实例时删除应 ErrRegionInUse, got %v", err)
	}
}

func TestBackfillInstanceRegion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	inst := newInstance("derp-old-1")
	inst.RegionID = 900
	inst.STUNPort = 3478
	inst.Region = ""
	if err := s.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	live := newInstance("derp-new-1")
	live.RegionID = 901
	live.STUNPort = 3479
	live.Region = "cd"
	if err := s.CreateInstance(ctx, live); err != nil {
		t.Fatal(err)
	}

	n, err := s.BackfillInstanceRegion(ctx, "wh")
	if err != nil || n != 1 {
		t.Fatalf("BackfillInstanceRegion: n=%d err=%v", n, err)
	}
	got, _ := s.GetInstance(ctx, "derp-old-1")
	if got.Region != "wh" {
		t.Errorf("历史实例应回填为 wh, got %q", got.Region)
	}
	got2, _ := s.GetInstance(ctx, "derp-new-1")
	if got2.Region != "cd" {
		t.Errorf("已有地区的实例不应被覆盖, got %q", got2.Region)
	}
	// 再跑一次应为 0。
	if n, err := s.BackfillInstanceRegion(ctx, "wh"); err != nil || n != 0 {
		t.Fatalf("二次回填应为 0, got n=%d err=%v", n, err)
	}
}

func TestRegion_EffectiveMaxContainers(t *testing.T) {
	reg := &Region{}
	if got := reg.EffectiveMaxContainers(100); got != 100 {
		t.Errorf("未配置时应沿用默认, got %d", got)
	}
	reg.MaxContainers = 50
	if got := reg.EffectiveMaxContainers(100); got != 50 {
		t.Errorf("配置后应覆盖默认, got %d", got)
	}
}
