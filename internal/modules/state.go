// Package modules 实现网关的治理模块集合。
//
// 每个模块是一个独立的治理单元，通过 gateway.Callbacks 在自己关心的回调点上
// 注册 handler；模块之间不直接调用，只通过 gateway.RequestContext 上的共享属性
// 传递中间结果。模块在列表中的排列顺序不影响执行顺序，顺序只由注册时的 order 决定。
//
// 与旧 internal/filters 的对应关系见 docs/MODULES.md §3.4。
package modules

import (
	"net/http"

	"gag/internal/config"
	"gag/internal/gateway"
	"gag/internal/observability/tracing"
	"gag/internal/policy"
	"gag/internal/routing"
)

// 共享属性键：跨模块协作的中间结果统一走这里。
//
// 不用类型化字段的原因是这些结果只被少数模块消费，为每一项都在
// RequestContext 上开一个导出字段会让框架知道太多模块内部约定。
const (
	// attrPrincipal 保存认证后的调用方身份（mod_auth 写入）。
	attrPrincipal = "modules.principal"
	// attrSpan 保存本请求的 trace span（mod_request_id 创建，mod_trace 收尾）。
	attrSpan = "modules.span"
	// attrCORSHeaders 保存需要在响应阶段合并的 CORS 头（mod_cors 请求段写入、响应段读取）。
	attrCORSHeaders = "modules.cors.headers"
	// attrRateLimitKey 保存本次限流使用的维度键，供指标打标签。
	attrRateLimitKey = "modules.ratelimit.key"
)

func setPrincipal(c *gateway.RequestContext, p *policy.Principal) {
	c.SetAttr(attrPrincipal, p)
}

func principalOf(c *gateway.RequestContext) *policy.Principal {
	if c == nil {
		return nil
	}
	v, _ := c.Attr(attrPrincipal)
	p, _ := v.(*policy.Principal)
	return p
}

func setSpan(c *gateway.RequestContext, s *tracing.Span) { c.SetAttr(attrSpan, s) }

func spanOf(c *gateway.RequestContext) *tracing.Span {
	if c == nil {
		return nil
	}
	v, _ := c.Attr(attrSpan)
	s, _ := v.(*tracing.Span)
	return s
}

func setCORSHeaders(c *gateway.RequestContext, h http.Header) {
	c.SetAttr(attrCORSHeaders, h)
}

func corsHeadersOf(c *gateway.RequestContext) http.Header {
	if c == nil {
		return nil
	}
	v, _ := c.Attr(attrCORSHeaders)
	h, _ := v.(http.Header)
	return h
}

func setRateLimitKey(c *gateway.RequestContext, key string) {
	c.SetAttr(attrRateLimitKey, key)
}

func rateLimitKeyOf(c *gateway.RequestContext) string {
	if c == nil {
		return ""
	}
	v, _ := c.Attr(attrRateLimitKey)
	s, _ := v.(string)
	return s
}

// State 是模块可见的数据平面运行时视图。
//
// 治理模块只依赖这三个方法：路由表用于路由解析，策略集用于治理决策，
// 快照用于取默认租户。server.State 已经满足该接口；
// 测试可以用内存实现替换，从而不启动监听器就能跑完整链路。
type State interface {
	Table() *routing.Table
	Policy(routeID string) *policy.Set
	Snapshot() *config.Snapshot
}

// setOf 返回当前路由的治理策略集合。
//
// 路由未匹配（RouteID 为空）时返回 nil：前置模块没有策略可查是正常情况，
// 调用方需要自行判断"没有策略"与"配了空策略"的区别。
func setOf(c *gateway.RequestContext, st State) *policy.Set {
	if st == nil || c.RouteID == "" {
		return nil
	}
	return st.Policy(c.RouteID)
}

// routeLabel 返回用于指标与日志的路由标签；未匹配路由统一记为 unmatched。
//
// 不在路由维度上用空字符串：Prometheus 的标签基数会随 404 流量无限增长。
func routeLabel(c *gateway.RequestContext) string {
	if c.RouteID == "" {
		return "unmatched"
	}
	return c.RouteID
}

// statusOf 返回当前响应状态码；尚无响应时按 200 计（finalize 的兜底值）。
func statusOf(c *gateway.RequestContext) int {
	if c.Response == nil {
		return http.StatusOK
	}
	if c.Response.Status == 0 {
		return http.StatusOK
	}
	return c.Response.Status
}
