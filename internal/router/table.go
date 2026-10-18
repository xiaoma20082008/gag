package router

import (
	"errors"
	"fmt"
	"sort"

	"gag/internal/config"
	"gag/internal/core"
)

type Table struct {
	version string
	tenant  string
	tenants map[string]*tenantNode
}

type tenantNode struct {
	name   string
	routes []*routeEntry
}

type routeEntry struct {
	id       string
	cluster  string
	priority int
	matcher  *Matcher
	cfg      *config.Route
	svc      *config.Service
}

func NewTable(snap *config.Snapshot) (*Table, error) {
	if snap == nil || snap.Config() == nil {
		return nil, errors.New("router: snapshot 为 nil")
	}

	cfg := snap.Config()
	t := &Table{
		version: snap.Version(),
		tenant:  snap.DefaultTenant(),
		tenants: make(map[string]*tenantNode, len(cfg.Tenants)),
	}

	for ti := range cfg.Tenants {
		tenantCfg := &cfg.Tenants[ti]
		tt := &tenantNode{name: tenantCfg.Name}
		for si := range tenantCfg.Services {
			svcCfg := &tenantCfg.Services[si]
			for ri := range svcCfg.Routes {
				routeCfg := &svcCfg.Routes[ri]
				matcher, err := NewMatcher(routeCfg)
				if err != nil {
					return nil, fmt.Errorf("router: %w", err)
				}
				tt.routes = append(tt.routes, &routeEntry{
					id:       routeCfg.ID,
					cluster:  routeCfg.Cluster,
					priority: routeCfg.Priority,
					matcher:  matcher,
					cfg:      routeCfg,
					svc:      svcCfg,
				})
			}
		}
		// 先比显式 priority，再比结构特异性（精确路径 > 前缀路径 > 仅规则）。
		sort.SliceStable(tt.routes, func(i, j int) bool {
			a, b := tt.routes[i], tt.routes[j]
			if a.priority != b.priority {
				return a.priority > b.priority
			}
			return a.matcher.Score() > b.matcher.Score()
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
func (t *Table) ResolveRoute(meta *core.RequestMeta) (*core.Decision, error) {
	return nil, fmt.Errorf("%w: %s %s", core.ErrRouteNotFound, meta.Method, meta.Path)
}
