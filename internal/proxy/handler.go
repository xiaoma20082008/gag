package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"gag/internal/core"
	"gag/internal/discovery"
	"gag/internal/middleware"
	gerr "gag/pkg/errors"
)

// upstreamOrder 让熔断判定成为 forward 回调点的第一道闸门：
// 它要在任何真正发起的尝试之前给出结论。
const upstreamOrder = 10

// Upstream 负责把请求送到下游，并决定失败之后怎么办。
//
// 它同时出现在两个层次上，这也是"回调点"模型的收益：
//   - HookForward 上的 handler 是每次尝试前的闸门（熔断判定）；
//   - Hooks.Forward/MaxAttempts/Retryable/Backoff 是框架内建钩子，
//     分别提供"一次尝试""最多几次""值不值得再试""等多久"。
//
// "打到哪"由 discovery.Resolver 在每次尝试前现场解析（集群分流 + 注册中心取实例
// + 负载均衡），所以重试会自然地换一个实例，而不是死磕同一个已经下线的节点。
type Upstream struct {
	policies middleware.Policies
	invokers *Registry
	resolver *discovery.Resolver
}

// NewUpstream 创建下游调用模块。
func NewUpstream(policies middleware.Policies, invokers *Registry, resolver *discovery.Resolver) *Upstream {
	return &Upstream{policies: policies, invokers: invokers, resolver: resolver}
}

func (m *Upstream) Name() string { return "mw_upstream" }

func (m *Upstream) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookForward, upstreamOrder, "mw_upstream", "upstream.invoke", m.onForward)
	return nil
}

// onForward 是每次尝试前的闸门：熔断打开就直接失败，不去消耗下游配额。
func (m *Upstream) onForward(c *core.RequestContext) core.Action {
	set := middleware.SetOf(c, m.policies)
	if set == nil {
		c.Fail(gerr.Errorf(gerr.CodeInternal, "路由 %s 缺少治理策略", c.RouteID))
		return core.ActionFinish
	}
	if set.Breaker == nil {
		return core.ActionGoOn
	}
	if err := set.Breaker.Allow(); err != nil {
		// 1 秒是给客户端的保守建议：半开探测什么时候放行由熔断器自己决定。
		c.Fail(gerr.New(gerr.CodeCircuitOpen, "下游熔断器已打开").
			WithCause(err).WithRetryAfter(time.Second))
		c.CircuitState = set.Breaker.State().String()
		return core.ActionFinish
	}
	c.CircuitState = set.Breaker.State().String()
	return core.ActionGoOn
}

// Attempt 执行一次下游调用
func (m *Upstream) Attempt(c *core.RequestContext) error {
	set := middleware.SetOf(c, m.policies)
	if set == nil {
		return gerr.Errorf(gerr.CodeInternal, "路由 %s 缺少治理策略", c.RouteID)
	}
	invoker := m.invokers.Get(c.Protocol)
	if invoker == nil {
		return gerr.Errorf(gerr.CodeNotImplemented, "协议 %s 未注册调用器", c.Protocol)
	}

	body, err := core.ReadBody(c)
	if err != nil {
		return err
	}

	// 超时同时挂在 ctx 与 req.Timeout 上：ctx 保证客户端断连时调用也一起取消，
	// req.Timeout 让 invoker 侧也能给出准确的超时错误。
	ctx, cancel := c.WithTimeout(set.Timeout)
	defer cancel()

	// 每次尝试都重新查找目标：注册中心里的实例变化对下一次重试立即生效，
	// 这也是把查找链放在调用期而不是路由期的全部意义。
	inst, cluster, version, err := m.resolver.Resolve(ctx, c)
	if err != nil {
		return err
	}
	c.Cluster = cluster
	c.Instance = inst.ID
	c.Upstream = inst.Address
	c.Version = version

	req := &InvocationRequest{
		Method:  c.Request.Method,
		URL:     buildURL(c),
		Header:  OutboundHeaders(c, set),
		Body:    body,
		Timeout: set.Timeout,
	}

	start := time.Now()
	resp, err := invoker.InvokeWithCtx(ctx, req)
	c.UpstreamLatency = time.Since(start)

	// 熔断只认"下游是否健康"：传输错误算失败，5xx 也算失败。
	// 只看 err 会把"下游稳定返回 500"记成成功，熔断器永远不会打开。
	if set.Breaker != nil {
		set.Breaker.Record(err == nil && resp != nil && resp.StatusCode < http.StatusInternalServerError)
	}
	if err != nil {
		if resp != nil {
			// 读响应体中途失败时保留已读到的部分，便于排查；最终状态码仍由错误决定。
			c.SetResponse(resp.StatusCode, resp.Header, resp.Body)
		}
		return wrapTransport(err)
	}

	c.UpstreamStatus = resp.StatusCode
	c.SetResponse(resp.StatusCode, resp.Header, resp.Body)

	// 下游返回可重试状态码时也按失败上报：重试循环据此决定是否再试一次。
	if retryableStatus(resp.StatusCode, set.RetryOnStatus) {
		return gerr.Errorf(gerr.CodeBadGateway, "下游返回 %d", resp.StatusCode).
			WithStatus(resp.StatusCode)
	}
	return nil
}

// MaxAttempts 返回最大尝试次数（Hooks.MaxAttempts 的实现）。
func (m *Upstream) MaxAttempts(c *core.RequestContext) int {
	set := middleware.SetOf(c, m.policies)
	if set == nil {
		return 1
	}
	if !idempotent(c.Request.Method) {
		// 非幂等方法一律不重试：重复一次 POST 可能就是重复一次扣费/下单。
		return 1
	}
	n := set.Retry.MaxAttempts
	if n < 1 {
		return 1
	}
	return n
}

// Retryable 判断本次失败是否值得再试（Hooks.Retryable 的实现）。
//
// err 由框架显式传入而不是从 c.Error() 反推：重试期间链路的错误必须是空的，
// 否则重试成功后 c.Error() 仍留着上一次的失败，访问日志会出现"200 + error_code"。
func (m *Upstream) Retryable(c *core.RequestContext, err error, _ int) bool {
	set := middleware.SetOf(c, m.policies)
	if set == nil || err == nil || !idempotent(c.Request.Method) {
		return false
	}
	if errors.Is(err, ErrTransport) || IsTimeout(err) {
		return true
	}
	var ge *gerr.Error
	if errors.As(err, &ge) {
		return retryableStatus(ge.Status, set.RetryOnStatus)
	}
	return false
}

// Backoff 返回第 attempt 次失败后的等待时间（Hooks.Backoff 的实现）。
func (m *Upstream) Backoff(c *core.RequestContext, attempt int) time.Duration {
	set := middleware.SetOf(c, m.policies)
	if set == nil {
		return 0
	}
	return set.Retry.BackoffFor(attempt)
}

// buildURL 拼出下游地址。
//
// Instance.Address 是 host:port（不带协议），因此这里补默认 http；
// 已带协议的地址（例如 mock 服务器）原样使用。
func buildURL(c *core.RequestContext) string {
	host := c.Upstream
	if host == "" {
		return ""
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return host + c.Request.URL.RequestURI()
}

// wrapTransport 把调用层错误翻译成客户端可理解的错误码。
//
// 超时与不可达分开：前者是下游慢，后者是下游没了，
// 客户端与监控的重试策略应该不一样。
func wrapTransport(err error) error {
	var ge *gerr.Error
	if errors.As(err, &ge) {
		return err
	}
	if IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return gerr.New(gerr.CodeUpstreamTimeout, "下游调用超时").WithCause(err)
	}
	return gerr.New(gerr.CodeBadGateway, "下游调用失败").WithCause(err)
}

// idempotent 判断方法是否幂等。
//
// 取 RFC 7231 的定义：GET/HEAD/OPTIONS/TRACE/PUT/DELETE 幂等，
// POST/PATCH 不幂等——网关不能替调用方决定"重复发送一次 POST"是否安全。
func idempotent(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPut, http.MethodDelete, http.MethodTrace:
		return true
	default:
		return false
	}
}

// retryableStatus 判断下游状态码是否值得重试。
//
// 502/503/504 是典型的"后面暂时不行"；加上配置里的 retry_on 让运维能按业务调整。
func retryableStatus(status int, extra []int) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	for _, s := range extra {
		if s == status {
			return true
		}
	}
	return false
}
