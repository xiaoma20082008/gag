package policy

import (
	"sync"
	"time"

	"gag/internal/config"
)

// RateLimiter 是限流器的统一抽象。
type RateLimiter interface {
	// Allow 返回是否放行；被拒绝时给出建议的重试等待时间。
	Allow(key string) (bool, time.Duration)
}

// NewRateLimiter 根据配置创建限流器。
func NewRateLimiter(p *config.RateLimitPolicy) RateLimiter {
	if p == nil || p.RPS <= 0 {
		return nil
	}
	switch p.Algorithm {
	case "sliding_window":
		return NewSlidingWindow(int(p.RPS), p.Window.Std())
	default:
		return NewTokenBucket(p.RPS, p.Burst)
	}
}

// ---------- token bucket ----------

// TokenBucket 是令牌桶限流器，允许一定程度的突发。
type TokenBucket struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*tokenBucketState
	now     func() time.Time
}

type tokenBucketState struct {
	tokens float64
	last   time.Time
}

// NewTokenBucket 创建令牌桶：rate 为每秒补充的令牌数，burst 为桶容量。
func NewTokenBucket(rate float64, burst int) *TokenBucket {
	if burst <= 0 {
		burst = 1
	}
	return &TokenBucket{
		rate:    rate,
		burst:   float64(burst),
		buckets: make(map[string]*tokenBucketState, 64),
		now:     time.Now,
	}
}

// Allow 尝试获取一个令牌。
func (l *TokenBucket) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	st, ok := l.buckets[key]
	if !ok {
		st = &tokenBucketState{tokens: l.burst, last: now}
		l.buckets[key] = st
	}
	elapsed := now.Sub(st.last).Seconds()
	if elapsed > 0 {
		st.tokens += elapsed * l.rate
		if st.tokens > l.burst {
			st.tokens = l.burst
		}
		st.last = now
	}
	if st.tokens >= 1 {
		st.tokens--
		return true, 0
	}
	wait := time.Duration((1 - st.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// GC 清理长时间未使用的 key，防止高基数标签导致内存膨胀。
func (l *TokenBucket) GC(ttl time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-ttl)
	removed := 0
	for k, st := range l.buckets {
		if st.last.Before(cutoff) {
			delete(l.buckets, k)
			removed++
		}
	}
	return removed
}

// Size 返回当前活跃 key 数量。
func (l *TokenBucket) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// ---------- sliding window ----------

// SlidingWindow 是滑动窗口限流器，按精确时间戳统计窗口内请求数。
type SlidingWindow struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string][]time.Time
	now     func() time.Time
}

// NewSlidingWindow 创建滑动窗口限流器。
func NewSlidingWindow(limit int, window time.Duration) *SlidingWindow {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Second
	}
	return &SlidingWindow{
		limit:   limit,
		window:  window,
		entries: make(map[string][]time.Time, 64),
		now:     time.Now,
	}
}

// Allow 统计窗口内的请求数并决定是否放行。
func (w *SlidingWindow) Allow(key string) (bool, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.now()
	cutoff := now.Add(-w.window)
	times := w.entries[key]
	// 窗口内的时间戳天然有序，从头裁剪即可。
	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	times = times[i:]

	if len(times) >= w.limit {
		w.entries[key] = times
		return false, times[0].Add(w.window).Sub(now)
	}
	w.entries[key] = append(times, now)
	return true, 0
}

// GC 清理空窗口。
func (w *SlidingWindow) GC(_ time.Duration) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := w.now().Add(-w.window)
	removed := 0
	for k, times := range w.entries {
		if len(times) == 0 || times[len(times)-1].Before(cutoff) {
			delete(w.entries, k)
			removed++
		}
	}
	return removed
}

// Size 返回当前活跃 key 数量。
func (w *SlidingWindow) Size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.entries)
}
