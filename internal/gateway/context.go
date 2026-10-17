package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"
)

// RequestContext 是一次请求在 filter 链中传递的可变上下文。
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
	err   *Error
	ctx   context.Context
}

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

// Fail 记录错误；只有第一个错误会被保留，后续 filter 不再覆盖。
func (c *RequestContext) Fail(err error) {
	if err == nil || c.err != nil {
		return
	}
	c.err = AsError(err)
}

// Failed 表示链路已进入错误状态。
func (c *RequestContext) Failed() bool { return c.err != nil }

// Error 返回链路上的结构化错误。
func (c *RequestContext) Error() *Error { return c.err }

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
		c.Fail(NewError(CodeInternal, "响应序列化失败").WithCause(err))
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
