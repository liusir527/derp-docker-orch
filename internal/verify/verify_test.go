package verify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"derp-orch/internal/store"
)

// newTestHandler 建内存 store + 一个活跃实例,返回 handler。
func newTestHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	inst := &store.Instance{
		ID: "derp-20260812-aaaa", Name: "derp-20260812-aaaa",
		Subdomain: "a.relay.test", RegionID: 900, RegionCode: "260812",
		STUNPort: 3478, ContainerName: "derp-20260812-aaaa",
		ExpiresAt: now.Add(2 * time.Hour), State: "active", CreatedAt: now,
	}
	if err := s.CreateInstance(context.Background(), inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	return New(s), s
}

func doVerify(t *testing.T, h *Handler, path, body string) (int, verifyResponse) {
	t.Helper()
	// PathValue 依赖路由匹配,必须经 mux 分发。
	mux := http.NewServeMux()
	mux.Handle("POST /verify/{instanceID}", h)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var resp verifyResponse
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应不是协议 JSON: %v", err)
		}
	}
	return rec.Code, resp
}

func TestVerify_ActiveAllows(t *testing.T) {
	h, _ := newTestHandler(t)
	code, resp := doVerify(t, h, "/verify/derp-20260812-aaaa", `{"NodePublic":"k1","Source":"1.2.3.4"}`)
	if code != 200 || !resp.Allow {
		t.Errorf("活跃实例应 Allow, code=%d resp=%+v", code, resp)
	}
}

func TestVerify_ExpiredByTimeDenies(t *testing.T) {
	h, _ := newTestHandler(t)
	// 把 handler 的时钟拨到实例过期之后。
	h.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	code, resp := doVerify(t, h, "/verify/derp-20260812-aaaa", `{"NodePublic":"k1","Source":"1.2.3.4"}`)
	if code != 200 || resp.Allow {
		t.Errorf("过期实例应拒绝, code=%d resp=%+v", code, resp)
	}
}

func TestVerify_NotFoundDenies(t *testing.T) {
	h, _ := newTestHandler(t)
	code, resp := doVerify(t, h, "/verify/derp-20990101-zzzz", `{"NodePublic":"k1","Source":"1.2.3.4"}`)
	if code != 200 || resp.Allow {
		t.Errorf("不存在实例应拒绝, code=%d resp=%+v", code, resp)
	}
}

func TestVerify_BadPathID(t *testing.T) {
	h, _ := newTestHandler(t)
	code, _ := doVerify(t, h, "/verify/AB!CD", `{"NodePublic":"k1"}`)
	if code != 400 {
		t.Errorf("非法路径 ID 应 400,实际 %d", code)
	}
}

func TestVerify_BadBody(t *testing.T) {
	h, _ := newTestHandler(t)
	code, _ := doVerify(t, h, "/verify/derp-20260812-aaaa", `{not json`)
	if code != 400 {
		t.Errorf("坏 JSON 应 400,实际 %d", code)
	}
}
