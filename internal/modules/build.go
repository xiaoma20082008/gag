package modules

import (
	"errors"
	"fmt"

	"gag/internal/config"
	"gag/internal/gateway"
	"gag/internal/observability/logging"
	"gag/internal/observability/metrics"
	"gag/internal/proxy"
)

// Options 是装配治理链所需的全部输入。
//
// 每一项都是可选的：没配的策略直接不挂载对应模块，
// 而不是塞一个"什么都不做"的模块占位——回调点上的空转也是开销。
type Options struct {
	// State 提供路由表与治理策略，是唯一必填项。
	State State
	// Logger 为 nil 时不挂载访问日志模块。
	Logger *logging.Logger
	// Metrics 为 nil 时不挂载指标模块。
	Metrics *metrics.Gateway
	// MaxBody 是进程级请求体上限（0 表示不限制）。
	MaxBody int64
	// IPAllow/IPDeny 是入口 IP 白/黑名单。
	IPAllow []string
	IPDeny  []string
	// CORS 为跨域策略；未配置 AllowOrigins 时不挂载。
	CORS *config.CORS
	// Invokers 是下游调用器注册表。
	Invokers *proxy.Registry
}

// Build 装配治理链：先让各模块在回调点上自注册，再配上框架内建钩子。
//
// 模块在这里的排列顺序不影响执行顺序——顺序由每个 Add 的 order 决定，
// 因此调整代码顺序不会悄悄改变线上行为。返回的 Chain 只读、可并发使用。
func Build(opts Options) (*gateway.Chain, error) {
	if opts.State == nil {
		return nil, errors.New("modules: State 为 nil")
	}

	upstream := NewUpstream(opts.State, opts.Invokers)

	mods := []gateway.Module{
		NewRequestID(),
		NewBodyLimit(opts.MaxBody),
		NewIP(opts.IPAllow, opts.IPDeny),
		NewCORS(opts.CORS),
		NewAuth(opts.State),
		NewValidate(opts.State),
		NewRateLimit(opts.State),
		upstream,
		NewResponse(opts.State),
		NewAccessLog(opts.Logger),
		NewMetrics(opts.Metrics),
		NewTrace(),
	}

	cbs := gateway.NewCallbacks()
	for _, m := range mods {
		if m == nil {
			continue
		}
		if err := m.Init(cbs); err != nil {
			return nil, fmt.Errorf("modules: %s 初始化失败: %w", m.Name(), err)
		}
	}

	return gateway.NewChain(cbs, gateway.Hooks{
		ResolveTenant: TenantResolver(opts.State),
		ResolveRoute:  RouteResolver(opts.State),
		Forward:       upstream.attempt,
		MaxAttempts:   upstream.maxAttempts,
		Retryable:     upstream.retryable,
		Backoff:       upstream.backoff,
	}), nil
}

// Topology 展开治理链的回调点与执行顺序，用于启动日志与排障。
func Topology(ch *gateway.Chain) map[string][]string {
	if ch == nil {
		return nil
	}
	return ch.Topology()
}
