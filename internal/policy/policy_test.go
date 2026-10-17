package policy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"gag/internal/config"
)

func TestTokenBucketRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewTokenBucket(10, 2)
	l.now = func() time.Time { return now }

	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("第 1 个请求应放行")
	}
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("第 2 个请求应放行")
	}
	ok, wait := l.Allow("k")
	if ok {
		t.Fatal("桶空后应拒绝")
	}
	if wait <= 0 {
		t.Fatalf("拒绝时应给出重试等待时间，实际 %v", wait)
	}

	// 10 rps 补充一个令牌需要 100ms
	now = now.Add(100 * time.Millisecond)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("补充令牌后应放行")
	}
	// 不同 key 互不影响
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("不同 key 应独立计数")
	}
}

func TestTokenBucketGC(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewTokenBucket(1, 1)
	l.now = func() time.Time { return now }
	l.Allow("a")
	l.Allow("b")
	now = now.Add(2 * time.Minute)
	if removed := l.GC(time.Minute); removed != 2 {
		t.Fatalf("GC 清理 %d 个 key, want 2", removed)
	}
	if l.Size() != 0 {
		t.Fatalf("GC 后仍有 %d 个 key", l.Size())
	}
}

func TestSlidingWindow(t *testing.T) {
	now := time.Unix(0, 0)
	w := NewSlidingWindow(2, time.Second)
	w.now = func() time.Time { return now }

	if ok, _ := w.Allow("k"); !ok {
		t.Fatal("窗口内第 1 个请求应放行")
	}
	if ok, _ := w.Allow("k"); !ok {
		t.Fatal("窗口内第 2 个请求应放行")
	}
	if ok, _ := w.Allow("k"); ok {
		t.Fatal("超过窗口上限应拒绝")
	}
	now = now.Add(1100 * time.Millisecond)
	if ok, _ := w.Allow("k"); !ok {
		t.Fatal("窗口滑过后应放行")
	}
}

func TestBreakerStateMachine(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBreaker(BreakerConfig{ErrorRate: 0.5, MinRequests: 4, OpenTimeout: 100 * time.Millisecond, HalfOpenRequests: 2})
	b.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		b.Record(false)
	}
	b.Record(true)
	if state := b.State(); state != StateOpen {
		t.Fatalf("错误率超阈值应熔断，实际 %s", state)
	}
	if err := b.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("熔断后应快速失败，实际 %v", err)
	}

	now = now.Add(200 * time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Fatalf("超时后应进入半开并放行探测，实际 %v", err)
	}
	b.Record(true)
	b.Record(true)
	if state := b.State(); state != StateClosed {
		t.Fatalf("探测成功后应恢复，实际 %s", state)
	}
}

func TestBreakerHalfOpenFailureReopens(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBreaker(BreakerConfig{ErrorRate: 0.5, MinRequests: 2, OpenTimeout: 50 * time.Millisecond, HalfOpenRequests: 2})
	b.now = func() time.Time { return now }

	b.Record(false)
	b.Record(false)
	if b.State() != StateOpen {
		t.Fatal("应进入熔断")
	}
	now = now.Add(100 * time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Fatal(err)
	}
	b.Record(false)
	if b.State() != StateOpen {
		t.Fatalf("半开探测失败应重新熔断，实际 %s", b.State())
	}
}

func TestRetryDo(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 3, Backoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, Jitter: 0}
	attempts := 0
	err := p.Do(context.Background(), func(_ context.Context, attempt int) error {
		attempts = attempt
		if attempt < 3 {
			return errors.New("boom")
		}
		return nil
	}, func(error) bool { return true })
	if err != nil {
		t.Fatalf("第 3 次应成功: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}

	attempts = 0
	err = p.Do(context.Background(), func(_ context.Context, attempt int) error {
		attempts = attempt
		return errors.New("fatal")
	}, func(error) bool { return false })
	if err == nil || attempts != 1 {
		t.Fatalf("不可重试错误应只执行一次，attempts=%d err=%v", attempts, err)
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := RetryPolicy{MaxAttempts: 5, Backoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	calls := 0
	err := p.Do(ctx, func(_ context.Context, _ int) error {
		calls++
		cancel()
		return errors.New("boom")
	}, func(error) bool { return true })
	if err == nil {
		t.Fatal("应返回最后一次错误")
	}
	if calls != 1 {
		t.Fatalf("ctx 取消后不应继续重试，calls=%d", calls)
	}
}

func signJWT(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(header + "." + body))
	return header + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestJWTAuthenticator(t *testing.T) {
	auth, err := NewAuthenticator(&config.AuthPolicy{
		Type:        "jwt",
		Secret:      "s3cr3t",
		Issuer:      "gag",
		Audience:    "gateway",
		TenantClaim: "tenant",
		Scopes:      []string{"infer"},
	})
	if err != nil {
		t.Fatal(err)
	}

	token := signJWT(t, "s3cr3t", map[string]any{
		"sub":    "u1",
		"tenant": "ai",
		"iss":    "gag",
		"aud":    "gateway",
		"exp":    time.Now().Add(time.Minute).Unix(),
		"scope":  "infer read",
	})
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	p, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("合法 token 应通过: %v", err)
	}
	if p.Subject != "u1" || p.Tenant != "ai" || !p.HasScope("infer") {
		t.Fatalf("principal = %+v", p)
	}

	tampered := token[:len(token)-2] + "xx"
	req.Header.Set("Authorization", "Bearer "+tampered)
	if _, err := auth.Authenticate(req); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("签名被篡改应拒绝，实际 %v", err)
	}

	expired := signJWT(t, "s3cr3t", map[string]any{
		"sub": "u1", "exp": time.Now().Add(-time.Minute).Unix(),
	})
	req.Header.Set("Authorization", "Bearer "+expired)
	if _, err := auth.Authenticate(req); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("过期 token 应拒绝，实际 %v", err)
	}

	req.Header.Del("Authorization")
	if _, err := auth.Authenticate(req); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("缺少 token 应拒绝，实际 %v", err)
	}
}

func TestAPIKeyAuthenticator(t *testing.T) {
	auth, err := NewAuthenticator(&config.AuthPolicy{Type: "api_key", Keys: []string{"k1"}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "k1")
	if _, err := auth.Authenticate(req); err != nil {
		t.Fatalf("合法 key 应通过: %v", err)
	}
	req.Header.Set("X-API-Key", "k2")
	if _, err := auth.Authenticate(req); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("非法 key 应拒绝，实际 %v", err)
	}
}
