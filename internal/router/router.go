package router

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"gag/internal/config"
	"gag/internal/core"
	"gag/internal/discovery"
	"gag/internal/middleware"
	"gag/internal/proxy"
	"gag/internal/telemetry/logging"
	"gag/internal/telemetry/metrics"
	gerr "gag/pkg/errors"
)

// Options 是装配治理链所需的全部输入。
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
	// Source 是实例来源（static / nacos），由调用期的 resolver 使用。
	Source discovery.DiscoveryClient
}

// State 是数据平面的运行时视图，实现 middleware.Policies。
//
// 治理模块只依赖这三个方法：路由表用于路由解析，策略集用于治理决策，
// 快照用于取默认租户。server.State 已经满足该接口；
// 测试可以用内存实现替换，从而不启动监听器就能跑完整链路。
type State interface {
	Table() *Table
	Policy(routeID string) *middleware.Set
	Snapshot() *config.Snapshot
}

// Build 装配治理链：先让各模块在回调点上自注册，再配上框架内建钩子。
//
// 模块在这里的排列顺序不影响执行顺序——顺序由每个 Add 的 order 决定，
// 因此调整代码顺序不会悄悄改变线上行为。返回的 Chain 只读、可并发使用。
func Build(opts Options) (*Chain, error) {
	if opts.State == nil {
		return nil, errors.New("router: State 为 nil")
	}

	upstream := proxy.NewUpstream(opts.State, opts.Invokers, discovery.NewResolver(opts.Source))

	mods := []core.Module{
		middleware.NewRequestID(),
		middleware.NewBodyLimit(opts.MaxBody),
		middleware.NewIP(opts.IPAllow, opts.IPDeny),
		middleware.NewCORS(opts.CORS),
		middleware.NewAuth(opts.State),
		middleware.NewValidate(opts.State),
		middleware.NewRateLimit(opts.State),
		upstream,
		proxy.NewTransformer(opts.State),
		middleware.NewAccessLog(opts.Logger),
		middleware.NewMetrics(opts.Metrics),
		middleware.NewTrace(),
	}

	cbs := core.NewCallbacks()
	for _, m := range mods {
		if m == nil {
			continue
		}
		if err := m.Init(cbs); err != nil {
			return nil, fmt.Errorf("router: %s 初始化失败: %w", m.Name(), err)
		}
	}

	return NewChain(cbs, Hooks{
		ResolveTenant: TenantResolver(opts.State),
		ResolveRoute:  RouteResolver(opts.State),
		Forward:       upstream.Attempt,
		MaxAttempts:   upstream.MaxAttempts,
		Retryable:     upstream.Retryable,
		Backoff:       upstream.Backoff,
	}), nil
}

// Topology 展开治理链的回调点与执行顺序，用于启动日志与排障。
func Topology(ch *Chain) map[string][]string {
	if ch == nil {
		return nil
	}
	return ch.Topology()
}

// TenantResolver 生成 Hooks.ResolveTenant。
//
// 租户解析刻意内建而不是做成模块：它决定后续所有模块看到的隔离边界，
// 做成模块会让"哪些模块能看到租户结果"变成注册顺序问题。
func TenantResolver(st State) func(*core.RequestContext) error {
	return func(c *core.RequestContext) error {
		name := c.Request.Header.Get("X-Tenant")
		if name == "" {
			name = c.Request.Header.Get("X-Tenant-Id")
		}
		if name == "" {
			// 未显式指定时用配置的默认租户（Snapshot 已处理"取第一个租户"的兜底）。
			name = st.Snapshot().DefaultTenant()
		}
		c.Tenant = name
		return nil
	}
}

// RouteResolver 生成 Hooks.ResolveRoute：租户 → 路由 → 服务。
//
// 这里只做内存匹配，不碰实例来源：集群与实例要到调用期（forward）才能确定，
// 否则每次请求在路由阶段就要查一次注册中心，灰度分流也拿不到认证后的身份。
func RouteResolver(st State) func(*core.RequestContext) error {
	return func(c *core.RequestContext) error {
		d, err := st.Table().ResolveRoute(&core.RequestMeta{
			Tenant:   c.Tenant,
			Method:   c.Request.Method,
			Host:     hostOnly(c.Request.Host),
			Path:     c.Request.URL.Path,
			Header:   c.Request.Header,
			Query:    c.Request.URL.Query(),
			ClientIP: c.ClientIP,
			// User 在路由期恒为空：身份要等 mw_auth 才知道。
			// 依赖用户的灰度分流因此只能按 client_ip 退，
			// 这是"先路由后认证"的固有代价——BFE 同样如此。
		})
		if err != nil {
			return mapLookupError(c, err)
		}

		c.Tenant = d.Tenant
		c.Service = d.Service
		c.RouteID = d.RouteID
		c.Protocol = d.Protocol
		c.SetDecision(d)

		return nil
	}
}

// mapLookupError 把查找链错误翻译成面向客户端的错误码。
//
// 查找错误与下游故障分开表述：前者是"这个请求不该进来"，
// 后者是"该进来但没送到"，客户端与监控要能区分。
func mapLookupError(c *core.RequestContext, err error) error {
	switch {
	case errors.Is(err, core.ErrTenantNotFound):
		return gerr.Errorf(gerr.CodePermissionDenied, "未知租户 %s", c.Tenant)
	case errors.Is(err, core.ErrRouteNotFound):
		return gerr.Errorf(gerr.CodeNotFound, "路由未匹配: %s %s", c.Request.Method, c.Request.URL.Path)
	case errors.Is(err, core.ErrClusterNotFound):
		return gerr.New(gerr.CodeBadGateway, "集群不存在")
	case errors.Is(err, core.ErrNoInstances):
		return gerr.New(gerr.CodeUpstreamUnavailable, "没有可用的下游实例")
	case errors.Is(err, core.ErrInstanceSource):
		// 注册中心不可达/超时：不是本请求的问题，是基础设施故障，
		// 与"没有实例"（容量问题）区分开。
		return gerr.New(gerr.CodeBadGateway, "实例来源查询失败").WithCause(err)
	default:
		return gerr.New(gerr.CodeInternal, "查找失败").WithCause(err)
	}
}

// hostOnly 去掉 host 里的端口，用于路由的主机匹配。
//
// 保留 IPv6 字面量中的冒号：[::1]:8080 → ::1。
func hostOnly(hostport string) string {
	if hostport == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	// 没带端口时 SplitHostPort 会报错，此时原值可能就是裸 IPv6。
	if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	}
	return hostport
}
