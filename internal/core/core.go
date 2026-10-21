// Package core 定义网关的领域模型与请求生命周期契约。
//
// 这里只放"被多个包同时需要"的东西：请求上下文、回调点、动作码、
// 插件注册表、领域模型与查找决策。它不依赖任何上层包（router /
// middleware / proxy / server），因此谁都可以 import 它，不会形成环。
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	gerr "gag/pkg/errors"
)

// RequestContext 是一次请求在治理链中传递的可变上下文。
type RequestContext struct {
	// Writer 保留原始响应写入器，供流式响应或 http.MaxBytesReader 使用。
	Writer  http.ResponseWriter
	Request *http.Request

	// Response 是链路产出，由 Chain 在全部回调点结束后统一写回。
	Response *Response

	// 请求标识与链路追踪。
	RequestID    string
	TraceID      string
	SpanID       string
	ParentSpanID string

	// 接入信息。
	ClientIP  string
	UserAgent string
	Tenant    string
	// Subject 是认证后的调用方标识；未认证时为空。
	// 灰度分流与一致性哈希按它做稳定映射。
	Subject string

	// 路由决策结果。
	RouteID  string
	Service  string
	Cluster  string
	Instance string
	Upstream string
	Version  string
	Protocol string

	// 治理结果，供访问日志与指标使用。
	AuthResult      string
	RateLimitResult string
	CircuitState    string
	UpstreamStatus  int
	UpstreamLatency time.Duration
	Attempts        int

	StartedAt time.Time

	// CloseConn 置位时不写响应，直接断开连接（ActionClose 的落点）。
	CloseConn bool

	attrs map[string]any
	err   *gerr.Error
	ctx   context.Context
	// decision 是路由期的查找结果，调用期据此解析集群与实例。
	decision *Decision
}

// SetDecision 记录路由期查找结果。
func (c *RequestContext) SetDecision(d *Decision) { c.decision = d }

// Decision 返回路由期查找结果；未完成路由时返回 nil。
func (c *RequestContext) Decision() *Decision { return c.decision }

// NewRequestContext 基于原始请求创建上下文。
func NewRequestContext(w http.ResponseWriter, r *http.Request) *RequestContext {
	return &RequestContext{
		Writer:    w,
		Request:   r,
		ClientIP:  ClientIP(r),
		UserAgent: r.UserAgent(),
		StartedAt: time.Now(),
		attrs:     make(map[string]any, 8),
		ctx:       r.Context(),
	}
}

// Context 返回底层 context。
func (c *RequestContext) Context() context.Context {
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// SetContext 替换底层 context（例如注入 span 或超时）。
func (c *RequestContext) SetContext(ctx context.Context) { c.ctx = ctx }

// WithTimeout 在请求 context 上派生超时。
func (c *RequestContext) WithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Context(), d)
}

// SetAttr 写入链路内共享的中间结果。
func (c *RequestContext) SetAttr(key string, val any) {
	if c.attrs == nil {
		c.attrs = make(map[string]any, 4)
	}
	c.attrs[key] = val
}

// Attr 读取中间结果。
func (c *RequestContext) Attr(key string) (any, bool) {
	v, ok := c.attrs[key]
	return v, ok
}

// GetString 读取字符串型中间结果。
func (c *RequestContext) GetString(key string) string {
	if v, ok := c.attrs[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Fail 记录错误；只有第一个错误会被保留，后续模块不再覆盖。
func (c *RequestContext) Fail(err error) {
	if err == nil || c.err != nil {
		return
	}
	c.err = gerr.AsError(err)
}

// Failed 表示链路已进入错误状态。
func (c *RequestContext) Failed() bool { return c.err != nil }

// Error 返回链路上的结构化错误。
func (c *RequestContext) Error() *gerr.Error { return c.err }

// Latency 返回从接受到当前的耗时。
func (c *RequestContext) Latency() time.Duration { return time.Since(c.StartedAt) }

// SetResponse 设置响应。
func (c *RequestContext) SetResponse(status int, header http.Header, body []byte) {
	if header == nil {
		header = http.Header{}
	}
	c.Response = &Response{Status: status, Header: header, Body: body}
}

// JSON 写入 JSON 响应。
func (c *RequestContext) JSON(status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		c.Fail(gerr.New(gerr.CodeInternal, "响应序列化失败").WithCause(err))
		return
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	c.SetResponse(status, h, body)
}

// Response 是链路最终要写回客户端的响应。
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// WriteTo 把响应写回客户端。
func (r *Response) WriteTo(w http.ResponseWriter) {
	if r == nil || w == nil {
		return
	}
	h := w.Header()
	for k, vs := range r.Header {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	status := r.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if len(r.Body) > 0 {
		_, _ = w.Write(r.Body)
	}
}

// DefaultErrorResponse 生成标准错误响应体。
func DefaultErrorResponse(c *RequestContext, err *gerr.Error) {
	if err == nil {
		err = gerr.New(gerr.CodeInternal, "unknown error")
	}
	body, _ := json.Marshal(map[string]string{
		"code":       string(err.Code),
		"message":    err.Message,
		"request_id": c.RequestID,
		"trace_id":   c.TraceID,
	})
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	if err.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(int(err.RetryAfter.Seconds()+0.5)))
	}
	c.SetResponse(err.Status, h, body)
}

// ReadBody 读完整请求体并把 Body 复位，使后续中间件可以再次读取。
//
// MaxBytesReader 的超限错误在这里转换成 413：它是治理拒绝，
// 不是传输故障，不该被当成下游错误重试。
func ReadBody(c *RequestContext) ([]byte, error) {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return nil, nil
	}
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, gerr.New(gerr.CodePayloadTooLarge, "请求体超过大小限制")
		}
		return nil, gerr.Errorf(gerr.CodeInvalidArgument, "读取请求体失败").WithCause(err)
	}
	// Body 已经读过一次，重试或后续中间件还要再读，因此复位成一个可重放的 reader。
	c.Request.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

// ClientIP 提取客户端地址，优先信任代理链写入的头。
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if rip := r.Header.Get("X-Real-IP"); rip != "" {
		return strings.TrimSpace(rip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
