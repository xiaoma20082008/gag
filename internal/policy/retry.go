package policy

import (
	"context"
	"math/rand/v2"
	"time"

	"gag/internal/config"
)

// RetryPolicy 描述指数退避重试。
type RetryPolicy struct {
	MaxAttempts int
	Backoff     time.Duration
	MaxBackoff  time.Duration
	Jitter      float64
}

// NewRetryPolicy 从配置构造重试策略。
func NewRetryPolicy(p *config.RetryPolicy) RetryPolicy {
	if p == nil {
		return RetryPolicy{MaxAttempts: 1}
	}
	rp := RetryPolicy{
		MaxAttempts: p.MaxAttempts,
		Backoff:     p.Backoff.Std(),
		MaxBackoff:  p.MaxBackoff.Std(),
		Jitter:      p.Jitter,
	}
	if rp.MaxAttempts < 1 {
		rp.MaxAttempts = 1
	}
	if rp.Backoff <= 0 {
		rp.Backoff = 50 * time.Millisecond
	}
	if rp.MaxBackoff <= 0 {
		rp.MaxBackoff = time.Second
	}
	if rp.Jitter < 0 || rp.Jitter > 1 {
		rp.Jitter = 0.2
	}
	return rp
}

// Do 执行 fn，直到成功、不可重试或次数用尽。
//
// retryable 为 nil 时视为所有错误都可重试；ctx 取消会立即返回。
func (p RetryPolicy) Do(ctx context.Context, fn func(ctx context.Context, attempt int) error, retryable func(error) bool) error {
	attempts := p.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}
		lastErr = fn(ctx, attempt)
		if lastErr == nil {
			return nil
		}
		if attempt == attempts {
			break
		}
		if retryable != nil && !retryable(lastErr) {
			break
		}
		if err := sleep(ctx, p.backoffFor(attempt)); err != nil {
			return lastErr
		}
	}
	return lastErr
}

// BackoffFor 返回第 attempt 次失败后应等待的时间。
//
// 计算规则导出给外部复用：网关的执行链自己驱动重试循环（每次尝试都会
// 触发 HookForward），但退避策略仍然是 policy 的职责，不应在两处各写一份。
func (p RetryPolicy) BackoffFor(attempt int) time.Duration {
	return p.backoffFor(attempt)
}

// backoffFor 计算第 attempt 次失败后的等待时间。
func (p RetryPolicy) backoffFor(attempt int) time.Duration {
	d := p.Backoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= p.MaxBackoff {
			d = p.MaxBackoff
			break
		}
	}
	if d > p.MaxBackoff {
		d = p.MaxBackoff
	}
	if p.Jitter > 0 {
		// 抖动避免大量请求在同一时刻重试形成二次峰值。
		factor := 1 + p.Jitter*(2*rand.Float64()-1)
		d = time.Duration(float64(d) * factor)
	}
	if d < 0 {
		d = 0
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
