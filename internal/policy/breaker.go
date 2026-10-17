package policy

import (
	"errors"
	"sync"
	"time"

	"gag/internal/config"
)

// ErrCircuitOpen 表示熔断器处于打开状态，请求应快速失败。
var ErrCircuitOpen = errors.New("policy: circuit breaker is open")

// State 是熔断器状态。
type State int

const (
	// StateClosed 正常放行。
	StateClosed State = iota
	// StateOpen 快速失败。
	StateOpen
	// StateHalfOpen 允许少量探测请求。
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

// BreakerConfig 是熔断参数。
type BreakerConfig struct {
	ErrorRate        float64
	MinRequests      int
	OpenTimeout      time.Duration
	HalfOpenRequests int
}

// Breaker 是错误率驱动的熔断器。
type Breaker struct {
	mu  sync.Mutex
	cfg BreakerConfig

	state    State
	requests int
	failures int

	openedAt     time.Time
	halfOpenBusy int
	halfOpenOK   int

	now func() time.Time
}

// NewBreaker 创建熔断器。
func NewBreaker(cfg BreakerConfig) *Breaker {
	if cfg.ErrorRate <= 0 || cfg.ErrorRate > 1 {
		cfg.ErrorRate = 0.5
	}
	if cfg.MinRequests <= 0 {
		cfg.MinRequests = 20
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = 5 * time.Second
	}
	if cfg.HalfOpenRequests <= 0 {
		cfg.HalfOpenRequests = 3
	}
	return &Breaker{cfg: cfg, now: time.Now}
}

// NewBreakerFromConfig 从 DSL 配置创建。
func NewBreakerFromConfig(p *config.BreakerPolicy) *Breaker {
	if p == nil {
		return nil
	}
	return NewBreaker(BreakerConfig{
		ErrorRate:        p.ErrorRate,
		MinRequests:      p.MinRequests,
		OpenTimeout:      p.OpenTimeout.Std(),
		HalfOpenRequests: p.HalfOpenRequests,
	})
}

// Allow 判断本次请求是否放行。
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateOpen:
		if b.now().Sub(b.openedAt) >= b.cfg.OpenTimeout {
			b.state = StateHalfOpen
			b.halfOpenBusy = 0
			b.halfOpenOK = 0
			return b.admitHalfOpen()
		}
		return ErrCircuitOpen
	case StateHalfOpen:
		return b.admitHalfOpen()
	default:
		return nil
	}
}

// admitHalfOpen 控制半开状态下的并发探测数，必须在持锁时调用。
func (b *Breaker) admitHalfOpen() error {
	if b.halfOpenBusy >= b.cfg.HalfOpenRequests {
		return ErrCircuitOpen
	}
	b.halfOpenBusy++
	return nil
}

// Record 上报一次调用结果。
func (b *Breaker) Record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateHalfOpen:
		if b.halfOpenBusy > 0 {
			b.halfOpenBusy--
		}
		if !success {
			b.open()
			return
		}
		b.halfOpenOK++
		if b.halfOpenOK >= b.cfg.HalfOpenRequests {
			b.close()
		}
	case StateClosed:
		b.requests++
		if !success {
			b.failures++
		}
		if b.requests >= b.cfg.MinRequests {
			rate := float64(b.failures) / float64(b.requests)
			if rate > b.cfg.ErrorRate {
				b.open()
			}
		}
	}
}

func (b *Breaker) open() {
	b.state = StateOpen
	b.openedAt = b.now()
	b.requests = 0
	b.failures = 0
	b.halfOpenBusy = 0
	b.halfOpenOK = 0
}

func (b *Breaker) close() {
	b.state = StateClosed
	b.requests = 0
	b.failures = 0
	b.halfOpenBusy = 0
	b.halfOpenOK = 0
}

// State 返回当前状态。
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	// 只读判断也要感知超时后的状态迁移，否则监控会看到滞后的 open。
	if b.state == StateOpen && b.now().Sub(b.openedAt) >= b.cfg.OpenTimeout {
		return StateHalfOpen
	}
	return b.state
}

// Stats 返回 (状态, 窗口内请求数, 窗口内失败数)。
func (b *Breaker) Stats() (State, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.requests, b.failures
}
