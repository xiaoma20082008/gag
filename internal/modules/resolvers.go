package modules

import (
	"errors"

	"gag/internal/gateway"
	"gag/internal/routing"
)

// TenantResolver 生成 Hooks.ResolveTenant。
//
// 租户解析刻意内建而不是做成模块：它决定后续所有模块看到的隔离边界，
// 做成模块会让"哪些模块能看到租户结果"变成注册顺序问题。
func TenantResolver(st State) func(*gateway.RequestContext) error {
	return func(c *gateway.RequestContext) error {
		name := c.Request.Header.Get("X-Tenant")
		if name == "" {
			name = c.Request.Header.Get("X-Tenant-Id")
		}
		if name == "" {
			// 未显式指定时用配置的默认租户（Snapshot 已处理"取第一个租户"的兜底）。
			name = st.Snapshot().DefaultTenant()
		}
		c.Tenant = name
		return nil
	}
}

// RouteResolver 生成 Hooks.ResolveRoute：查表、回填上下文、应用路径改写。
func RouteResolver(st State) func(*gateway.RequestContext) error {
	return func(c *gateway.RequestContext) error {
		res, err := st.Table().Resolve(&routing.RequestMeta{
			Tenant:   c.Tenant,
			Method:   c.Request.Method,
			Host:     hostOnly(c.Request.Host),
			Path:     c.Request.URL.Path,
			Header:   c.Request.Header,
			Query:    c.Request.URL.Query(),
			ClientIP: c.ClientIP,
			// User 在路由期恒为空：身份要等 mod_auth 才知道。
			// 依赖用户的灰度分流与一致性哈希因此只能按 client_ip 退，
			// 这是"先路由后认证"的固有代价——BFE 同样如此。
		})
		if err != nil {
			return mapRoutingError(c, err)
		}

		c.Tenant = res.Tenant
		c.Service = res.Service
		c.Cluster = res.Cluster
		c.Instance = res.Instance
		c.RouteID = res.RouteID
		c.Version = res.Version
		c.Protocol = res.Protocol
		c.Upstream = res.Addr

		if res.Rewrite != nil {
			c.Request.URL.Path = routing.RewritePath(c.Request.URL.Path, res.Rewrite)
		}
		return nil
	}
}

// mapRoutingError 把路由层错误翻译成面向客户端的错误码。
//
// 路由错误与下游故障分开表述：前者是"这个请求不该进来"，
// 后者是"该进来但没送到"，客户端与监控要能区分。
func mapRoutingError(c *gateway.RequestContext, err error) error {
	switch {
	case errors.Is(err, routing.ErrTenantNotFound):
		return gateway.Errorf(gateway.CodePermissionDenied, "未知租户 %s", c.Tenant)
	case errors.Is(err, routing.ErrRouteNotFound):
		return gateway.Errorf(gateway.CodeNotFound, "路由未匹配: %s %s", c.Request.Method, c.Request.URL.Path)
	case errors.Is(err, routing.ErrClusterNotFound):
		return gateway.Errorf(gateway.CodeBadGateway, "集群不存在")
	case errors.Is(err, routing.ErrNoHealthyInstance):
		return gateway.Errorf(gateway.CodeUpstreamUnavailable, "没有健康的下游实例")
	default:
		return gateway.NewError(gateway.CodeInternal, "路由查找失败").WithCause(err)
	}
}
