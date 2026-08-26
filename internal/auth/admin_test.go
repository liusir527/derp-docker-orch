package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestMgr() *Manager {
	return New(Options{
		Username:       "admin",
		Password:       "secret",
		SessionTTL:     time.Hour,
		LoginRateLimit: 3,
		LoginWindow:    time.Minute,
		MaxFails:       2,
		LockDuration:   time.Minute,
	})
}

func TestLoginSuccess(t *testing.T) {
	m := newTestMgr()
	tok, err := m.Login("admin", "secret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok == "" {
		t.Fatal("token 为空")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	if _, ok := m.Authenticate(r); !ok {
		t.Fatal("有效 session 应通过")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	m := newTestMgr()
	if _, err := m.Login("admin", "nope"); err != ErrInvalidCredentials {
		t.Fatalf("期望 ErrInvalidCredentials, 实际 %v", err)
	}
}

func TestLoginLockout(t *testing.T) {
	m := newTestMgr()
	// 连续错 2 次(MaxFails=2)→ 触发锁定。
	for i := 0; i < 2; i++ {
		m.Login("admin", "bad")
	}
	locked, _ := m.Locked("admin")
	if !locked {
		t.Fatal("应已锁定")
	}
}

func TestIPRateLimit(t *testing.T) {
	m := newTestMgr()
	ip := "1.2.3.4"
	for i := 0; i < 3; i++ {
		if !m.CheckIPRate(ip) {
			t.Fatalf("第 %d 次应允许", i+1)
		}
	}
	if m.CheckIPRate(ip) {
		t.Fatal("第 4 次应被限速")
	}
	// 其他 IP 不受影响。
	if !m.CheckIPRate("5.6.7.8") {
		t.Fatal("其他 IP 应允许")
	}
}

func TestLogoutInvalidates(t *testing.T) {
	m := newTestMgr()
	tok, _ := m.Login("admin", "secret")
	m.Logout(tok)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	if _, ok := m.Authenticate(r); ok {
		t.Fatal("登出后 session 应失效")
	}
}
