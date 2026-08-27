// Package auth admin 会话认证与防爆破。
// 参考 Headscale-Admin-Pro auth.py:IP 每 60s 限速 + 失败锁定。
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"
)

// SessionCookie 会话 cookie 名。
const SessionCookie = "derp_admin_session"

// Options 认证配置。
type Options struct {
	Username       string
	SessionTTL     time.Duration
	LoginRateLimit int           // IP 每窗口最大登录尝试
	LoginWindow    time.Duration // 窗口
	MaxFails       int           // 同用户连续失败锁定阈值
	LockDuration   time.Duration // 锁定时长
	// Verify 密码校验函数(支持 DB bcrypt)。由组装方注入。
	Verify func(password string) bool
}

// Manager admin 认证。
type Manager struct {
	opts   Options
	mu     sync.Mutex
	sess   map[string]session // token → session
	ipHits map[string][]time.Time
	fails  map[string]failState // 用户名 → 失败计数
	// ipFailed 记录哪些 IP 曾登录失败 → 后续登录需验证码。
	ipFailed map[string]bool
}

type session struct {
	username string
	expiry   time.Time
}

type failState struct {
	count       int
	lockedUntil time.Time
}

// New 创建认证管理器。
func New(opts Options) *Manager {
	return &Manager{
		opts:     opts,
		sess:     map[string]session{},
		ipHits:   map[string][]time.Time{},
		fails:    map[string]failState{},
		ipFailed: map[string]bool{},
	}
}

// Login 校验用户名+密码,成功返回 session token。
// 密码校验委托 opts.Verify(DB bcrypt);旧 opts.Password 字段已移除。
func (m *Manager) Login(username, password string) (string, error) {
	if username != m.opts.Username {
		return "", ErrInvalidCredentials
	}
	if m.opts.Verify == nil || !m.opts.Verify(password) {
		m.recordFail(username)
		return "", ErrInvalidCredentials
	}
	m.resetFails(username)
	token, err := m.newToken()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.sess[token] = session{username: username, expiry: time.Now().Add(m.opts.SessionTTL)}
	m.mu.Unlock()
	return token, nil
}

// NeedsCaptcha 该 IP 是否需要验证码(曾登录失败过)。
func (m *Manager) NeedsCaptcha(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ipFailed[ip]
}

// MarkIPFailed 记录该 IP 登录失败,后续登录需验证码。
func (m *Manager) MarkIPFailed(ip string) {
	m.mu.Lock()
	m.ipFailed[ip] = true
	m.mu.Unlock()
}

// ResetIPFailure 登录成功后清除该 IP 失败标记。
func (m *Manager) ResetIPFailure(ip string) {
	m.mu.Lock()
	delete(m.ipFailed, ip)
	m.mu.Unlock()
}

// Logout 销毁 session。
func (m *Manager) Logout(token string) {
	m.mu.Lock()
	delete(m.sess, token)
	m.mu.Unlock()
}

// Authenticate 校验请求中的 session cookie,返回用户名与 ok。
func (m *Manager) Authenticate(r *http.Request) (username string, ok bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, exists := m.sess[c.Value]
	if !exists || time.Now().After(s.expiry) {
		delete(m.sess, c.Value)
		return "", false
	}
	return s.username, true
}

// CheckIPRate IP 限速。窗口内达到上限返回 false;否则记录本次。
func (m *Manager) CheckIPRate(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-m.opts.LoginWindow)
	hit := m.ipHits[ip]
	kept := hit[:0]
	for _, t := range hit {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	m.ipHits[ip] = kept
	if len(kept) >= m.opts.LoginRateLimit {
		return false
	}
	m.ipHits[ip] = append(m.ipHits[ip], now)
	return true
}

// Locked 用户是否被锁定;返回剩余锁定时间。
func (m *Manager) Locked(username string) (bool, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.fails[username]
	if !ok {
		return false, 0
	}
	if time.Now().Before(f.lockedUntil) {
		return true, time.Until(f.lockedUntil)
	}
	return false, 0
}

// ClientIP 取客户端 IP(优先 X-Real-IP,兼容 nginx 反代)。
func ClientIP(r *http.Request) string {
	if x := r.Header.Get("X-Real-IP"); x != "" {
		if h, _, err := net.SplitHostPort(x); err == nil {
			return h
		}
		return x
	}
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		return x
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (m *Manager) recordFail(username string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.fails[username]
	f.count++
	if f.count >= m.opts.MaxFails {
		f.lockedUntil = time.Now().Add(m.opts.LockDuration)
		f.count = 0
	}
	m.fails[username] = f
}

func (m *Manager) resetFails(username string) {
	m.mu.Lock()
	delete(m.fails, username)
	m.mu.Unlock()
}

func (m *Manager) newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ErrInvalidCredentials 用户名或密码错误。
var ErrInvalidCredentials = errInvalidCredentials{}

type errInvalidCredentials struct{}

func (errInvalidCredentials) Error() string { return "用户名或密码错误" }
