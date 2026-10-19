package proxy

import (
	"net/http"
	"testing"
)

func TestIdempotentAndRetryableStatus(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "OPTIONS", "PUT", "DELETE", "TRACE"} {
		if !idempotent(m) {
			t.Errorf("%s 应视为幂等", m)
		}
	}
	for _, m := range []string{"POST", "PATCH", "CONNECT"} {
		if idempotent(m) {
			t.Errorf("%s 不应视为幂等", m)
		}
	}
	if !retryableStatus(http.StatusServiceUnavailable, nil) {
		t.Error("503 应默认可重试")
	}
	if !retryableStatus(418, []int{418}) {
		t.Error("retry_on 中的状态码应可重试")
	}
	if retryableStatus(http.StatusOK, nil) {
		t.Error("200 不应可重试")
	}
}

func TestStripHopByHop(t *testing.T) {
	h := http.Header{}
	h.Set("Connection", "X-Secret, close")
	h.Set("X-Secret", "leak")
	h.Set("Keep-Alive", "timeout=5")
	h.Set("Transfer-Encoding", "chunked")
	h.Set("X-Keep", "yes")
	StripHopByHop(h)

	for _, gone := range []string{"Connection", "X-Secret", "Keep-Alive", "Transfer-Encoding"} {
		if h.Get(gone) != "" {
			t.Errorf("%s 应被删除", gone)
		}
	}
	if h.Get("X-Keep") != "yes" {
		t.Error("end-to-end 头必须保留")
	}
}
