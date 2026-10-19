package middleware

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"gag/internal/config"
	"gag/internal/core"
)

// CORS 处理跨域请求：预检短路 + 响应头合并。
//
// 它是"一个中间件挂两个回调点"的样板：请求段决定要不要短路，
// 响应段把头补到最终响应上——两段必须一起改，放在同一个文件里就不会漏。
type CORS struct{ cfg *config.CORS }

// NewCORS 创建 CORS 中间件；未配置 AllowOrigins 时返回 nil（不挂载）。
func NewCORS(cfg *config.CORS) core.Module {
	if cfg == nil || len(cfg.AllowOrigins) == 0 {
		return nil
	}
	return &CORS{cfg: cfg}
}

func (m *CORS) Name() string { return "mw_cors" }

func (m *CORS) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookBeforeRoute, 40, "mw_cors", "cors.request", m.onRequest)
	cbs.Add(core.HookReadResponse, 50, "mw_cors", "cors.response", m.onResponse)
	return nil
}

func (m *CORS) onRequest(c *core.RequestContext) core.Action {
	origin := c.Request.Header.Get("Origin")
	if origin == "" || !m.allowOrigin(origin) {
		// 非跨域请求或来源不在白名单：不写 CORS 头，也不短路，
		// 由浏览器按同源策略自行拦截。
		return core.ActionGoOn
	}

	setCORSHeaders(c, m.headersFor(origin))

	if c.Request.Method == http.MethodOptions &&
		c.Request.Header.Get("Access-Control-Request-Method") != "" {
		// 预检请求没有业务语义，直接 204 短路，不必再走租户/路由/转发。
		c.SetResponse(http.StatusNoContent, m.headersFor(origin), nil)
		return core.ActionRespond
	}
	return core.ActionGoOn
}

func (m *CORS) onResponse(c *core.RequestContext) core.Action {
	h := corsHeadersOf(c)
	if len(h) == 0 || c.Response == nil {
		return core.ActionGoOn
	}
	if c.Response.Header == nil {
		c.Response.Header = http.Header{}
	}
	for k, vs := range h {
		for _, v := range vs {
			c.Response.Header.Add(k, v)
		}
	}
	return core.ActionGoOn
}

func (m *CORS) allowOrigin(origin string) bool {
	if slices.Contains(m.cfg.AllowOrigins, "*") {
		return true
	}
	return slices.Contains(m.cfg.AllowOrigins, origin)
}

// headersFor 生成一组 CORS 响应头。
//
// AllowCredentials 为真时必须回显具体 Origin：规范不允许
// `Access-Control-Allow-Origin: *` 与携带凭证的请求同时出现。
func (m *CORS) headersFor(origin string) http.Header {
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
