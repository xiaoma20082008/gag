// Package middleware 实现网关的治理中间件。
//
// 每个文件是一个治理关注点（鉴权、跨域、限流、日志、熔断……），
// 通过 core.Callbacks 在自己关心的回调点上注册 handler；中间件之间不直接调用，
// 只通过 core.RequestContext 上的共享属性传递中间结果。
// 注册时的 order 决定执行顺序，文件与切片中的排列顺序不影响行为。
package middleware

import (
	"net/http"

	"gag/internal/core"
	"gag/internal/telemetry/tracing"
)

// 共享属性键：跨中间件协作的中间结果统一走这里。
//
// 不用类型化字段的原因是这些结果只被少数中间件消费，为每一项都在
// RequestContext 上开一个导出字段会让框架知道太多模块内部约定。
const (
	// attrPrincipal 保存认证后的调用方身份（auth 写入）。
	attrPrincipal = "middleware.principal"
	// attrSpan 保存本请求的 trace span（request_id 创建，trace 收尾）。
	attrSpan = "middleware.span"
	// attrCORSHeaders 保存需要在响应阶段合并的 CORS 头（cors 请求段写入、响应段读取）。
	attrCORSHeaders = "middleware.cors.headers"
	// attrRateLimitKey 保存本次限流使用的维度键，供指标打标签。
	attrRateLimitKey = "middleware.ratelimit.key"
)

// Policies 提供路由对应的治理策略集合。
//
// server.State 满足该接口；测试可以用内存实现替换。
type Policies interface {
	Policy(routeID string) *Set
}

func setPrincipal(c *core.RequestContext, p *Principal) {
	c.SetAttr(attrPrincipal, p)
}

func principalOf(c *core.RequestContext) *Principal {
	if c == nil {
		return nil
	}
	v, _ := c.Attr(attrPrincipal)
	p, _ := v.(*Principal)
	return p
}

// SubjectOf 返回认证后的调用方标识；未认证时为空。
//
// 一致性哈希与灰度分流都需要它：分流放在调用期，就是为了拿到真实身份。
func SubjectOf(c *core.RequestContext) string {
	if c == nil {
		return ""
	}
	return c.Subject
}

// SetOf 返回当前路由的治理策略集合。
//
// 导出给 proxy 用：转发同样需要超时、重试与请求头改写策略。
func SetOf(c *core.RequestContext, st Policies) *Set { return setOf(c, st) }

func setSpan(c *core.RequestContext, s *tracing.Span) { c.SetAttr(attrSpan, s) }

func spanOf(c *core.RequestContext) *tracing.Span {
	if c == nil {
		return nil
	}
	v, _ := c.Attr(attrSpan)
	s, _ := v.(*tracing.Span)
	return s
}

func setCORSHeaders(c *core.RequestContext, h http.Header) {
	c.SetAttr(attrCORSHeaders, h)
}

func corsHeadersOf(c *core.RequestContext) http.Header {
	if c == nil {
		return nil
	}
	v, _ := c.Attr(attrCORSHeaders)
	h, _ := v.(http.Header)
	return h
}

func setRateLimitKey(c *core.RequestContext, key string) {
	c.SetAttr(attrRateLimitKey, key)
}

func rateLimitKeyOf(c *core.RequestContext) string {
	if c == nil {
		return ""
	}
	v, _ := c.Attr(attrRateLimitKey)
	s, _ := v.(string)
	return s
}

// setOf 返回当前路由的治理策略集合。
//
// 路由未匹配（RouteID 为空）时返回 nil：前置中间件没有策略可查是正常情况，
// 调用方需要自行判断"没有策略"与"配了空策略"的区别。
func setOf(c *core.RequestContext, st Policies) *Set {
	if st == nil || c.RouteID == "" {
		return nil
	}
	return st.Policy(c.RouteID)
}

// routeLabel 返回用于指标与日志的路由标签；未匹配路由统一记为 unmatched。
//
// 不在路由维度上用空字符串：Prometheus 的标签基数会随 404 流量无限增长。
func routeLabel(c *core.RequestContext) string {
	if c.RouteID == "" {
		return "unmatched"
	}
	return c.RouteID
}

// statusOf 返回当前响应状态码；尚无响应时按 200 计（finalize 的兜底值）。
func statusOf(c *core.RequestContext) int {
	if c.Response == nil {
		return http.StatusOK
	}
	if c.Response.Status == 0 {
		return http.StatusOK
	}
	return c.Response.Status
}
