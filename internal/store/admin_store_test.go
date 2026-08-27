package store

import (
	"context"
	"testing"
)

func TestAdminPassword_Lifecycle(t *testing.T) {
	s := newRedeemTestStore(t)
	ctx := context.Background()

	// 未初始化 → 校验失败。
	if s.VerifyAdminPassword(ctx, "any") {
		t.Fatal("未初始化密码不应通过")
	}
	// 初始化。
	if err := s.InitAdminPassword(ctx, "seed-pass"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 二次初始化不应覆盖。
	if err := s.InitAdminPassword(ctx, "other"); err != nil {
		t.Fatalf("二次 Init: %v", err)
	}
	if !s.VerifyAdminPassword(ctx, "seed-pass") {
		t.Fatal("初始密码应通过")
	}
	if s.VerifyAdminPassword(ctx, "other") {
		t.Fatal("二次 Init 不应覆盖原密码")
	}
	// 改密码。
	if err := s.UpdateAdminPassword(ctx, "seed-pass", "new-pass"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !s.VerifyAdminPassword(ctx, "new-pass") {
		t.Fatal("新密码应通过")
	}
	if s.VerifyAdminPassword(ctx, "seed-pass") {
		t.Fatal("旧密码应失效")
	}
	// 旧密码错 → 拒绝。
	if err := s.UpdateAdminPassword(ctx, "wrong", "x"); err == nil {
		t.Fatal("旧密码错误应拒绝")
	}
}
