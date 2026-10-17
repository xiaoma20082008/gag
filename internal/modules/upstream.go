package modules

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"gag/internal/gateway"
	"gag/internal/observability/tracing"
	"gag/internal/policy"
	"gag/internal/proxy"
)

// upstreamOrder 让熔断判定成为 forward 回调点的第一道闸门：
// 它要在任何真正发起的尝试之前给出结论。
const upstreamOrder = 10

// UpstreamModule 负责把请求送到下游，并决定失败之后怎么办。
//
// 它同时出现在两个层次上，这也是"回调点"模型的收益：
//   - HookForward 上的 handler 是每次尝试前的闸门（熔断判定）；
//   - Hooks.Forward/MaxAttempts/Retryable/Backoff 是框架内建钩子，
//     分别提供"一次尝试""最多几次""值不值得再试""等多久"。
//
// 因此每发起一次尝试，整条链都能通过 HookForward 看到它，
// 而不是像旧实现那样把重试循环藏在一个 filter 内部。
type UpstreamModule struct {
	st       State
	invokers *proxy.Registry
}

// NewUpstream 创建下游调用模块。
func NewUpstream(st State, invokers *proxy.Registry) *UpstreamModule {
	return &UpstreamModule{st: st, invokers: invokers}
}

func (m *UpstreamModule) Name() string { return "mod_upstream" }

func (m *UpstreamModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookForward, upstreamOrder, "mod_upstream", "upstream.invoke", m.onForward)
	return nil
}

// onForward 是每次尝试前的闸门：熔断打开就直接失败，不去消耗下游配额。
func (m *UpstreamModule) onForward(c *gateway.RequestContext) gateway.Action {
	set := setOf(c, m.st)
	if set == nil {
		c.Fail(gateway.Errorf(gateway.CodeInternal, "路由 %s 缺少治理策略", c.RouteID))
		return gateway.ActionFinish
	}
	if set.Breaker == nil {
		return gateway.ActionGoOn
	}
	if err := set.Breaker.Allow(); err != nil {
		// 1 秒是给客户端的保守建议：半开探测什么时候放行由熔断器自己决定。
		c.Fail(gateway.NewError(gateway.CodeCircuitOpen, "下游熔断器已打开").
			WithCause(err).WithRetryAfter(time.Second))
		c.CircuitState = set.Breaker.State().String()
		return gateway.ActionFinish
	}
	c.CircuitState = set.Breaker.State().String()
	return gateway.ActionGoOn
}

// attempt 执行一次下游调用（Hooks.Forward 的实现）。
//
// 返回值非 nil 表示这次尝试失败；是否重试由 Hooks.Retryable 决定，
// 因此这里不自己循环，也不自己退避。
func (m *UpstreamModule) attempt(c *gateway.RequestContext) error {
	set := setOf(c, m.st)
	if set == nil {
		return gateway.Errorf(gateway.CodeInternal, "路由 %s 缺少治理策略", c.RouteID)
	}
	invoker := m.invokers.Get(c.Protocol)
	if invoker == nil {
		return gateway.Errorf(gateway.CodeNotImplemented, "协议 %s 未注册调用器", c.Protocol)
	}

	body, err := readBody(c)
	if err != nil {
		return err
	}

	// 超时同时挂在 ctx 与 req.Timeout 上：ctx 保证客户端断连时调用也一起取消，
	// req.Timeout 让 invoker 侧也能给出准确的超时错误。
	ctx, cancel := c.WithTimeout(set.Timeout)
	defer cancel()

	req := &proxy.InvocationRequest{
		Method:  c.Request.Method,
		URL:     buildURL(c),
		Header:  outboundHeaders(c, set),
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
		return gateway.Errorf(gateway.CodeBadGateway, "下游返回 %d", resp.StatusCode).
			WithStatus(resp.StatusCode)
	}
	return nil
}

// maxAttempts 返回最大尝试次数（Hooks.MaxAttempts 的实现）。
func (m *UpstreamModule) maxAttempts(c *gateway.RequestContext) int {
	set := setOf(c, m.st)
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

// retryable 判断本次失败是否值得再试（Hooks.Retryable 的实现）。
//
// err 由框架显式传入而不是从 c.Error() 反推：重试期间链路的错误必须是空的，
// 否则重试成功后 c.Error() 仍留着上一次的失败，访问日志会出现"200 + error_code"。
// 幂等性是第一道闸：即使路由配了 max_attempts，非幂等方法也不重试。
func (m *UpstreamModule) retryable(c *gateway.RequestContext, err error, _ int) bool {
	set := setOf(c, m.st)
	if set == nil || err == nil || !idempotent(c.Request.Method) {
		return false
	}
	if errors.Is(err, proxy.ErrTransport) || proxy.IsTimeout(err) {
		return true
	}
	var ge *gateway.Error
	if errors.As(err, &ge) {
		return retryableStatus(ge.Status, set.RetryOnStatus)
	}
	return false
}

// backoff 返回第 attempt 次失败后的等待时间（Hooks.Backoff 的实现）。
//
// 直接复用 policy 的指数退避实现：重试循环搬到了框架里，
// 但退避策略仍然是策略层的职责，不该出现第二份实现。
func (m *UpstreamModule) backoff(c *gateway.RequestContext, attempt int) time.Duration {
	set := setOf(c, m.st)
	if set == nil {
		return 0
	}
	return set.Retry.BackoffFor(attempt)
}

// buildURL 拼出下游地址。
//
// Instance.Addr 是 host:port（不带协议），因此这里补默认 http；
// 已带协议的地址（例如 mock 服务器）原样使用。
func buildURL(c *gateway.RequestContext) string {
	host := c.Upstream
	if host == "" {
		return ""
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return host + c.Request.URL.RequestURI()
}

// hopByHopHeaders 是 end-to-end 之外的头，转发与回传都必须剥掉。
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Proxy-Connection":    {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

// outboundHeaders 构造发往下游的请求头。
//
// HeadPolicy 在 build 期就挂到路由的 policy.Set 上，因此这里能直接改：
// 请求头改写与转发在同一层完成，不需要额外的回调点。
func outboundHeaders(c *gateway.RequestContext, set *policy.Set) http.Header {
	h := c.Request.Header.Clone()
	stripHopByHop(h)

	// X-Forwarded-For 追加而不是覆盖：前面可能还有别的代理。
	if prior := h.Get("X-Forwarded-For"); prior != "" {
		h.Set("X-Forwarded-For", prior+", "+c.ClientIP)
	} else if c.ClientIP != "" {
		h.Set("X-Forwarded-For", c.ClientIP)
	}
	h.Set("X-Forwarded-Host", c.Request.Host)
	h.Set("X-Request-Id", c.RequestID)
	tracing.Inject(h, c.TraceID, c.SpanID)

	if set != nil && set.Headers != nil {
		for k, v := range set.Headers.RequestAdd {
			h.Set(k, v)
		}
		for _, k := range set.Headers.RequestRemove {
			h.Del(k)
		}
	}
	return h
}

// stripHopByHop 删除所有 hop-by-hop 头，以及 Connection 头里点名要删的头。
//
// 必须先读出 Connection 的内容再删它自己：Connection 本身就是 hop-by-hop 头，
// 顺序反了就永远拿不到那份"点名清单"，被点名的头会泄漏到下游。
func stripHopByHop(h http.Header) {
	named := h.Values("Connection")
	for name := range h {
		if _, ok := hopByHopHeaders[http.CanonicalHeaderKey(name)]; ok {
			h.Del(name)
		}
	}
	for _, v := range named {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
}

// wrapTransport 把调用层错误翻译成客户端可理解的错误码。
//
// 超时与不可达分开：前者是下游慢，后者是下游没了，
// 客户端与监控的重试策略应该不一样。
func wrapTransport(err error) error {
	var ge *gateway.Error
	if errors.As(err, &ge) {
		return err
	}
	if proxy.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return gateway.NewError(gateway.CodeUpstreamTimeout, "下游调用超时").WithCause(err)
	}
	return gateway.NewError(gateway.CodeBadGateway, "下游调用失败").WithCause(err)
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
