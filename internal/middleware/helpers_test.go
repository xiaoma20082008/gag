package middleware

import (
	"net/netip"
	"testing"
	"time"
)

func TestParsePrefixes(t *testing.T) {
	got := parsePrefixes([]string{"10.0.0.1", "192.168.0.0/16", "", " 172.16.0.5 ", "not-an-ip"})
	if len(got) != 3 {
		t.Fatalf("应解析出 3 条规则，实际 %d: %v", len(got), got)
	}
	if !got[0].Contains(mustAddr(t, "10.0.0.1")) {
		t.Error("裸地址应按 /32 处理")
	}
	if !got[1].Contains(mustAddr(t, "192.168.5.5")) {
		t.Error("CIDR 应生效")
	}
	if got[0].Contains(mustAddr(t, "10.0.0.2")) {
		t.Error("裸地址只应匹配自身")
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("解析 %q: %v", s, err)
	}
	return addr
}

func TestRetryAfterFloor(t *testing.T) {
	if got := retryAfterFloor(900 * time.Millisecond); got != time.Second {
		t.Errorf("retryAfterFloor(900ms) = %v, want 1s", got)
	}
	if got := retryAfterFloor(0); got != 0 {
		t.Errorf("retryAfterFloor(0) = %v, want 0", got)
	}
	if got := retryAfterFloor(2500 * time.Millisecond); got != 3*time.Second {
		t.Errorf("retryAfterFloor(2.5s) = %v, want 3s", got)
	}
}

func TestMatchContentType(t *testing.T) {
	if !matchContentType("application/json; charset=utf-8", []string{"application/json"}) {
		t.Error("应忽略 charset 参数")
	}
	if !matchContentType("APPLICATION/JSON", []string{"application/json"}) {
		t.Error("应大小写不敏感")
	}
	if matchContentType("", []string{"application/json"}) {
		t.Error("空 Content-Type 不应匹配")
	}
	if matchContentType("text/plain", []string{"application/json"}) {
		t.Error("不同类型不应匹配")
	}
}
