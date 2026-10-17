package modules

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"gag/internal/config"
	"gag/internal/gateway"
	"gag/internal/observability/tracing"
)

// RequestIDModule 生成请求标识与链路上下文。
//
// 放在 BeforeRoute(10)：请求 id 与 trace id 是后续所有模块与日志的关联键，
// 必须第一个就位；一旦 ID 过滤之后才生成，被拒绝的请求就带不上标识。
type RequestIDModule struct{}

// NewRequestID 创建 request id 模块；该模块没有可配置项，永远生效。
func NewRequestID() gateway.Module { return &RequestIDModule{} }

func (m *RequestIDModule) Name() string { return "mod_request_id" }

func (m *RequestIDModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookBeforeRoute, 10, "mod_request_id", "request.id", m.handle)
	return nil
}

func (m *RequestIDModule) handle(c *gateway.RequestContext) gateway.Action {
	// 接入方显式带的 request id 直接沿用，但只接受合理长度，
	// 防止把任意长的字符串透传到日志与下游头里。
	if raw := strings.TrimSpace(c.Request.Header.Get("X-Request-Id")); raw != "" && len(raw) <= 128 {
		c.RequestID = raw
	}
	if c.RequestID == "" {
		c.RequestID = tracing.NewSpanID()
	}

	// traceparent 是 W3C 约定；上游没带就由本请求新建一个 trace。
	traceID, parentSpanID := tracing.Extract(c.Request.Header)
	ctx := c.Context()
	if traceID != "" {
		ctx = tracing.WithTrace(ctx, traceID, parentSpanID)
	}
	ctx, span := tracing.StartSpan(ctx, "gateway")
	c.SetContext(ctx)

	c.TraceID = span.TraceID
	c.SpanID = span.SpanID
	c.ParentSpanID = span.ParentID
	setSpan(c, span)
	return gateway.ActionGoOn
}

// ---------- 请求体大小限制 ----------

// BodyLimitModule 在入口限制请求体大小。
//
// 用 http.MaxBytesReader 而不是读完整体再判断：超大请求在读的过程中就该被掐断，
// 否则网关要先替它付一遍内存与带宽。
type BodyLimitModule struct{ limit int64 }

// NewBodyLimit 创建请求体限制模块；limit <= 0 表示不限制（不挂载）。
func NewBodyLimit(limit int64) gateway.Module {
	if limit <= 0 {
		return nil
	}
	return &BodyLimitModule{limit: limit}
}

func (m *BodyLimitModule) Name() string { return "mod_body_limit" }

func (m *BodyLimitModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookBeforeRoute, 20, "mod_body_limit", "body.limit", m.handle)
	return nil
}

func (m *BodyLimitModule) handle(c *gateway.RequestContext) gateway.Action {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return gateway.ActionGoOn
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, m.limit)
	return gateway.ActionGoOn
}

// ---------- IP 黑白名单 ----------

// IPModule 按客户端 IP 做准入控制。
type IPModule struct {
	allow []netip.Prefix
	deny  []netip.Prefix
}

// NewIP 创建 IP 黑白名单模块；两张单都为空时返回 nil（不挂载）。
func NewIP(allow, deny []string) gateway.Module {
	m := &IPModule{allow: parsePrefixes(allow), deny: parsePrefixes(deny)}
	if len(m.allow) == 0 && len(m.deny) == 0 {
		return nil
	}
	return m
}

func (m *IPModule) Name() string { return "mod_ip" }

func (m *IPModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookBeforeRoute, 30, "mod_ip", "ip.filter", m.handle)
	return nil
}

func (m *IPModule) handle(c *gateway.RequestContext) gateway.Action {
	ip, err := netip.ParseAddr(c.ClientIP)
	if err != nil {
		// 解析不出地址（unix socket、带 zone 的 IPv6）时不猜，交给后续治理。
		return gateway.ActionGoOn
	}
	// 黑名单优先：显式拒绝不应被白名单的宽松规则救回来。
	for _, p := range m.deny {
		if p.Contains(ip) {
			c.Fail(gateway.NewError(gateway.CodePermissionDenied, "客户端 IP 被拒绝访问"))
			return gateway.ActionFinish
		}
	}
	if len(m.allow) > 0 {
		for _, p := range m.allow {
			if p.Contains(ip) {
				return gateway.ActionGoOn
			}
		}
		c.Fail(gateway.NewError(gateway.CodePermissionDenied, "客户端 IP 不在白名单"))
		return gateway.ActionFinish
	}
	return gateway.ActionGoOn
}

// parsePrefixes 解析 IP 列表，同时接受 CIDR 与裸地址。
//
// 裸地址按 /32 或 /128 处理：写成 10.0.0.1 的运维意图显然是"这一台"。
func parsePrefixes(list []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p)
			continue
		}
		if addr, err := netip.ParseAddr(item); err == nil {
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
		}
	}
	return out
}

// ---------- CORS ----------

// CORSModule 处理跨域请求：预检短路 + 响应头合并。
//
// 它是"一个模块挂两个回调点"的样板：请求段决定要不要短路，
// 响应段把头补到最终响应上——旧实现里这两段被 pre.cors 与 post.response 分割，
// 中间隔着一整条转发链，改一处必须记着改另一处。
type CORSModule struct{ cfg *config.CORS }

// NewCORS 创建 CORS 模块；未配置 AllowOrigins 时返回 nil（不挂载）。
func NewCORS(cfg *config.CORS) gateway.Module {
	if cfg == nil || len(cfg.AllowOrigins) == 0 {
		return nil
	}
	return &CORSModule{cfg: cfg}
}

func (m *CORSModule) Name() string { return "mod_cors" }

func (m *CORSModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookBeforeRoute, 40, "mod_cors", "cors.request", m.onRequest)
	cbs.Add(gateway.HookReadResponse, 50, "mod_cors", "cors.response", m.onResponse)
	return nil
}

func (m *CORSModule) onRequest(c *gateway.RequestContext) gateway.Action {
	origin := c.Request.Header.Get("Origin")
	if origin == "" || !m.allowOrigin(origin) {
		// 非跨域请求或来源不在白名单：不写 CORS 头，也不短路，
		// 由浏览器按同源策略自行拦截。
		return gateway.ActionGoOn
	}

	setCORSHeaders(c, m.headersFor(origin))

	if c.Request.Method == http.MethodOptions &&
		c.Request.Header.Get("Access-Control-Request-Method") != "" {
		// 预检请求没有业务语义，直接 204 短路，不必再走租户/路由/转发。
		c.SetResponse(http.StatusNoContent, m.headersFor(origin), nil)
		return gateway.ActionRespond
	}
	return gateway.ActionGoOn
}

func (m *CORSModule) onResponse(c *gateway.RequestContext) gateway.Action {
	h := corsHeadersOf(c)
	if len(h) == 0 || c.Response == nil {
		return gateway.ActionGoOn
	}
	if c.Response.Header == nil {
		c.Response.Header = http.Header{}
	}
	for k, vs := range h {
		for _, v := range vs {
			c.Response.Header.Add(k, v)
		}
	}
	return gateway.ActionGoOn
}

func (m *CORSModule) allowOrigin(origin string) bool {
	if slices.Contains(m.cfg.AllowOrigins, "*") {
		return true
	}
	return slices.Contains(m.cfg.AllowOrigins, origin)
}

// headersFor 生成一组 CORS 响应头。
//
// AllowCredentials 为真时必须回显具体 Origin：规范不允许
// `Access-Control-Allow-Origin: *` 与携带凭证的请求同时出现。
func (m *CORSModule) headersFor(origin string) http.Header {
	h := http.Header{}
	if m.cfg.AllowCredentials || !slices.Contains(m.cfg.AllowOrigins, "*") {
		h.Set("Access-Control-Allow-Origin", origin)
	} else {
		h.Set("Access-Control-Allow-Origin", "*")
	}
	h.Add("Vary", "Origin")
	if len(m.cfg.AllowMethods) > 0 {
		h.Set("Access-Control-Allow-Methods", strings.Join(m.cfg.AllowMethods, ", "))
	}
	if len(m.cfg.AllowHeaders) > 0 {
		h.Set("Access-Control-Allow-Headers", strings.Join(m.cfg.AllowHeaders, ", "))
	}
	if len(m.cfg.ExposeHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(m.cfg.ExposeHeaders, ", "))
	}
	if m.cfg.AllowCredentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	if d := m.cfg.MaxAge.Std(); d > 0 {
		h.Set("Access-Control-Max-Age", strconv.Itoa(int(d.Seconds())))
	}
	return h
}

// ---------- 请求体读取 ----------

// readBody 读完整请求体并把 Body 复位，使后续模块可以再次读取。
//
// MaxBytesReader 的超限错误在这里转换成 413：它是治理拒绝，
// 不是传输故障，不该被当成下游错误重试。
func readBody(c *gateway.RequestContext) ([]byte, error) {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return nil, nil
	}
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, gateway.NewError(gateway.CodePayloadTooLarge, "请求体超过大小限制")
		}
		return nil, gateway.Errorf(gateway.CodeInvalidArgument, "读取请求体失败").WithCause(err)
	}
	// Body 已经读过一次，重试或后续模块还要再读，因此复位成一个可重放的 reader。
	c.Request.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
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
