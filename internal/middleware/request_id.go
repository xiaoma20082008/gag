package middleware

import (
	"strings"

	"gag/internal/core"
	"gag/internal/telemetry/tracing"
)

// RequestID 生成请求标识与链路上下文。
//
// 挂在 BeforeRoute(10)：请求 id 与 trace id 是后续所有中间件与日志的关联键，
// 必须第一个就位；一旦 IP 过滤之后才生成，被拒绝的请求就带不上标识。
type RequestID struct{}

// NewRequestID 创建 request id 中间件；该中间件没有可配置项，永远生效。
func NewRequestID() core.Module { return &RequestID{} }

func (m *RequestID) Name() string { return "mw_request_id" }

func (m *RequestID) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookBeforeRoute, 10, "mw_request_id", "request.id", m.handle)
	return nil
}

func (m *RequestID) handle(c *core.RequestContext) core.Action {
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
	return core.ActionGoOn
}
