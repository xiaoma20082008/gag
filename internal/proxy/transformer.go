package proxy

import (
	"net/http"
	"strings"

	"gag/internal/core"
	"gag/internal/middleware"
	"gag/internal/telemetry/tracing"
)

// Transformer 在拿到下游响应后做最后一道规范化。
//
// 与 CORS 同处 read_response 回调点，但 order 更小：先把响应收拾干净，
// 再让 CORS 之类的中间件往上加头。
type Transformer struct{ policies middleware.Policies }

// NewTransformer 创建响应规范化模块。
func NewTransformer(policies middleware.Policies) core.Module {
	return &Transformer{policies: policies}
}

func (m *Transformer) Name() string { return "mw_transformer" }

func (m *Transformer) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookReadResponse, 10, "mw_transformer", "response.normalize", m.handle)
	return nil
}

func (m *Transformer) handle(c *core.RequestContext) core.Action {
	if c.Response == nil {
		// finalize 通常已经保证这里非空；这是给自定义装配留的兜底。
		if err := c.Error(); err != nil {
			core.DefaultErrorResponse(c, err)
		} else {
			c.SetResponse(http.StatusOK, http.Header{}, nil)
		}
	}

	// 下游返回的状态码与响应体原样透传：网关替下游改写业务状态码
	// 会让客户端无法判断请求到底成功没有。这里只清理传输层遗留的头。
	StripHopByHop(c.Response.Header)

	set := middleware.SetOf(c, m.policies)
	if set == nil || set.Headers == nil {
		return core.ActionGoOn
	}
	if c.Response.Header == nil {
		c.Response.Header = http.Header{}
	}
	for k, v := range set.Headers.ResponseAdd {
		c.Response.Header.Set(k, v)
	}
	for _, k := range set.Headers.ResponseRemove {
		c.Response.Header.Del(k)
	}
	return core.ActionGoOn
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

// OutboundHeaders 构造发往下游的请求头。
//
// HeadPolicy 在编译期就挂到路由的策略集合上，因此这里能直接改：
// 请求头改写与转发在同一层完成，不需要额外的回调点。
func OutboundHeaders(c *core.RequestContext, set *middleware.Set) http.Header {
	h := c.Request.Header.Clone()
	StripHopByHop(h)

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

// StripHopByHop 删除所有 hop-by-hop 头，以及 Connection 头里点名要删的头。
//
// 必须先读出 Connection 的内容再删它自己：Connection 本身就是 hop-by-hop 头，
// 顺序反了就永远拿不到那份"点名清单"，被点名的头会泄漏到下游。
func StripHopByHop(h http.Header) {
	if h == nil {
		return
	}
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
