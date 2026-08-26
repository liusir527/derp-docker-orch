package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func newRedeemTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedCode(t *testing.T, s *Store, code string) {
	t.Helper()
	_, err := s.CreateRedeemCodes(context.Background(), []RedeemCode{
		{Code: code, DurationValue: 30, DurationUnit: "day", CreatedBy: "admin", CreatedAt: time.Now()},
	})
	if err != nil {
		t.Fatalf("CreateRedeemCodes: %v", err)
	}
}

func TestReserveConsume(t *testing.T) {
	s := newRedeemTestStore(t)
	seedCode(t, s, "DERP-AAAA-1111")
	ctx := context.Background()

	tok, err := s.ReserveRedeemCode(ctx, "DERP-AAAA-1111")
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if tok == "" {
		t.Fatal("token 为空")
	}
	// 未消费时再次 reserve 应失败(被占用)。
	if _, err := s.ReserveRedeemCode(ctx, "DERP-AAAA-1111"); !errors.Is(err, ErrCodeUnavailable) {
		t.Fatalf("重复预留应拒绝, got %v", err)
	}
	// 消费。
	if err := s.ConsumeRedeemCode(ctx, tok, "derp-20260826-ab12"); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	// 已用码不可再换。
	if _, err := s.ReserveRedeemCode(ctx, "DERP-AAAA-1111"); !errors.Is(err, ErrCodeUnavailable) {
		t.Fatalf("已用码应拒绝, got %v", err)
	}
	// 用量查询。
	c, err := s.GetRedeemCode(ctx, "DERP-AAAA-1111")
	if err != nil {
		t.Fatal(err)
	}
	if c.UsedByInstanceID == nil || *c.UsedByInstanceID != "derp-20260826-ab12" {
		t.Fatalf("used_by 未记录: %+v", c.UsedByInstanceID)
	}
}

func TestReserve_ReleaseAllowsReuse(t *testing.T) {
	s := newRedeemTestStore(t)
	seedCode(t, s, "DERP-BBBB-2222")
	ctx := context.Background()

	tok, err := s.ReserveRedeemCode(ctx, "DERP-BBBB-2222")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseRedeemCode(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveRedeemCode(ctx, "DERP-BBBB-2222"); err != nil {
		t.Fatalf("释放后应可再预留, got %v", err)
	}
}

func TestConcurrentReserve_SameCodeOnlyOneWins(t *testing.T) {
	s := newRedeemTestStore(t)
	seedCode(t, s, "DERP-CCCC-3333")
	ctx := context.Background()

	var wg sync.WaitGroup
	success := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ReserveRedeemCode(ctx, "DERP-CCCC-3333")
			success <- (err == nil)
		}()
	}
	wg.Wait()
	close(success)
	wins := 0
	for ok := range success {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("并发预留应仅一个成功, 实际 %d", wins)
	}
}

func TestListCodes_StatusFilter(t *testing.T) {
	s := newRedeemTestStore(t)
	ctx := context.Background()
	seedCode(t, s, "DERP-DDDD-4444")
	seedCode(t, s, "DERP-EEEE-5555")

	_, total, err := s.ListRedeemCodes(ctx, "all", 0, 0)
	if err != nil || total != 2 {
		t.Fatalf("all: total=%d err=%v", total, err)
	}
	unused, ut, _ := s.ListRedeemCodes(ctx, "unused", 0, 0)
	if ut != 2 || len(unused) != 2 {
		t.Fatalf("unused: %d %d", ut, len(unused))
	}
	// 用一个。
	tok, _ := s.ReserveRedeemCode(ctx, "DERP-DDDD-4444")
	_ = s.ConsumeRedeemCode(ctx, tok, "inst-1")
	used, got, _ := s.ListRedeemCodes(ctx, "used", 0, 0)
	if got != 1 || len(used) != 1 {
		t.Fatalf("used: %d %d", got, len(used))
	}
}
