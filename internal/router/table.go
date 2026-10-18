package router

import (
	"errors"
	"fmt"
	"sort"

	"gag/internal/config"
	"gag/internal/core"
)

// Table 是配置编译出的只读路由索引。
//
// 与 Snapshot 一一对应：配置变更时整体重建并原子替换，
// 因此查找路径上不需要任何写锁，也不存在读到半份配置的窗口。
//
// 表里只有"请求属于哪条路由"：集群与实例是运行时数据，
// 由调用期的 discovery.Resolver 解析，不进这张表。
type Table struct {
	version       string
	defaultTenant string
	tenants       map[string]*tenantNode
}

type tenantNode struct {
	name   string
	routes []*routeEntry
}

type routeEntry struct {
	id      string
	cluster string
	matcher *Matcher
	cfg     *config.Route
	svc     *config.Service
}

// NewTable 从快照编译路由索引。
func NewTable(snap *config.Snapshot) (*Table, error) {
	if snap == nil || snap.Config() == nil {
		return nil, errors.New("router: snapshot 为 nil")
	}

	cfg := snap.Config()
	t := &Table{
		version:       snap.Version(),
		defaultTenant: snap.DefaultTenant(),
		tenants:       make(map[string]*tenantNode, len(cfg.Tenants)),
	}

	for ti := range cfg.Tenants {
		tenantCfg := &cfg.Tenants[ti]
		tt := &tenantNode{name: tenantCfg.Name}
		for si := range tenantCfg.Services {
			svcCfg := &tenantCfg.Services[si]
			for ri := range svcCfg.Routes {
				routeCfg := &svcCfg.Routes[ri]
				tt.routes = append(tt.routes, &routeEntry{
					id:      routeCfg.ID,
					cluster: routeCfg.Cluster,
					matcher: NewMatcher(routeCfg),
					cfg:     routeCfg,
					svc:     svcCfg,
				})
			}
		}
		// 精确路径优先、条件更多的路由优先。
		sort.SliceStable(tt.routes, func(i, j int) bool {
			return tt.routes[i].matcher.Score() > tt.routes[j].matcher.Score()
		})
		t.tenants[tenantCfg.Name] = tt
	}
	return t, nil
}

// Version 返回索引对应的配置版本。
func (t *Table) Version() string { return t.version }

// TenantCount 返回租户数量。
func (t *Table) TenantCount() int { return len(t.tenants) }

// RouteCount 返回编译后的路由总数。
func (t *Table) RouteCount() int {
	n := 0
	for _, tt := range t.tenants {
		n += len(tt.routes)
	}
	return n
}

// ResolveRoute 路由期查找：租户 → 路由 → 服务。
//
// 只做内存匹配，不访问实例来源，因此可以在 found_route 回调点执行——
// 认证、限流等治理模块都按路由配置，必须先知道路由才知道该用哪套策略。
func (t *Table) ResolveRoute(meta *core.RequestMeta) (*core.Decision, error) {
	tenantName := meta.Tenant
	if tenantName == "" {
		tenantName = t.defaultTenant
	}
	tt, ok := t.tenants[tenantName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", core.ErrTenantNotFound, tenantName)
	}

	for _, r := range tt.routes {
		if r.matcher.Match(meta) {
			return &core.Decision{
				Tenant:     tenantName,
				Service:    r.svc.Name,
				RouteID:    r.id,
				Protocol:   r.svc.Protocol,
				Route:      r.cfg,
				ServiceCfg: r.svc,
			}, nil
		}
	}
	return nil, fmt.Errorf("%w: %s %s", core.ErrRouteNotFound, meta.Method, meta.Path)
}
