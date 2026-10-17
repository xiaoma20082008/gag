package routing

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gag/internal/config"
	"gag/internal/lb"
)

// 路由查找错误，调用方据此映射为 404/503。
var (
	ErrTenantNotFound    = errors.New("routing: tenant not found")
	ErrRouteNotFound     = errors.New("routing: no route matched")
	ErrClusterNotFound   = errors.New("routing: cluster not found")
	ErrNoHealthyInstance = errors.New("routing: no healthy instance")
)

// Resolution 是一次路由查找的完整结果。
type Resolution struct {
	Tenant   string
	Service  string
	Cluster  string
	Instance string
	Addr     string

	RouteID  string
	Version  string
	Protocol string

	Route      *config.Route
	ClusterCfg *config.Cluster
	Rewrite    *config.Rewrite
}

// Table 是配置编译后的只读路由索引。
//
// Table 与 Snapshot 一一对应：配置变更时整体重建并原子替换，
// 因此查找路径上不需要任何写锁。
type Table struct {
	version       string
	defaultTenant string
	tenants       map[string]*tenantTable
	health        *Health
}

type tenantTable struct {
	name     string
	routes   []*compiledRoute
	services map[string]*serviceTable
}

type serviceTable struct {
	cfg      *config.Service
	clusters map[string]*clusterTable
}

type clusterTable struct {
	cfg      *config.Cluster
	balancer lb.Balancer
}

type compiledRoute struct {
	cfg     *config.Route
	service *serviceTable
	matcher *Matcher
}

// NewTable 从快照编译路由表。
func NewTable(snap *config.Snapshot, health *Health) (*Table, error) {
	if snap == nil || snap.Config() == nil {
		return nil, errors.New("routing: snapshot 为 nil")
	}
	if health == nil {
		health = NewHealth()
	}
	cfg := snap.Config()
	t := &Table{
		version:       snap.Version(),
		defaultTenant: snap.DefaultTenant(),
		tenants:       make(map[string]*tenantTable, len(cfg.Tenants)),
		health:        health,
	}

	for ti := range cfg.Tenants {
		tenantCfg := &cfg.Tenants[ti]
		tt := &tenantTable{name: tenantCfg.Name, services: make(map[string]*serviceTable, len(tenantCfg.Services))}
		for si := range tenantCfg.Services {
			svcCfg := &tenantCfg.Services[si]
			st := &serviceTable{cfg: svcCfg, clusters: make(map[string]*clusterTable, len(svcCfg.Clusters))}
			for ci := range svcCfg.Clusters {
				clusterCfg := &svcCfg.Clusters[ci]
				nodes := make([]lb.Node, 0, len(clusterCfg.Instances))
				for _, ins := range clusterCfg.Instances {
					nodes = append(nodes, lb.Node{Addr: ins.Addr, Weight: ins.Weight, Meta: ins.Metadata})
				}
				balancer, err := lb.New(clusterCfg.LB, nodes)
				if err != nil {
					return nil, fmt.Errorf("租户 %s 服务 %s 集群 %s: %w", tenantCfg.Name, svcCfg.Name, clusterCfg.Name, err)
				}
				st.clusters[clusterCfg.Name] = &clusterTable{cfg: clusterCfg, balancer: balancer}
			}
			for ri := range svcCfg.Routes {
				routeCfg := &svcCfg.Routes[ri]
				tt.routes = append(tt.routes, &compiledRoute{
					cfg:     routeCfg,
					service: st,
					matcher: NewMatcher(routeCfg),
				})
			}
			tt.services[svcCfg.Name] = st
		}
		// 精确路径优先、条件更多的路由优先。
		sort.SliceStable(tt.routes, func(i, j int) bool {
			return tt.routes[i].matcher.Score() > tt.routes[j].matcher.Score()
		})
		t.tenants[tenantCfg.Name] = tt
	}
	return t, nil
}

// Version 返回路由表对应的配置版本。
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

// Resolve 执行 Tenant -> Service -> Cluster -> Instance 的分层查找。
func (t *Table) Resolve(meta *RequestMeta) (*Resolution, error) {
	tenantName := meta.Tenant
	if tenantName == "" {
		tenantName = t.defaultTenant
	}
	tt, ok := t.tenants[tenantName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTenantNotFound, tenantName)
	}

	var matched *compiledRoute
	for _, cr := range tt.routes {
		if cr.matcher.Match(meta) {
			matched = cr
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("%w: %s %s", ErrRouteNotFound, meta.Method, meta.Path)
	}

	clusterName := selectCluster(matched.cfg, meta)
	if clusterName == "" {
		clusterName = matched.cfg.Cluster
	}
	ct, ok := matched.service.clusters[clusterName]
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s", ErrClusterNotFound, matched.service.cfg.Name, clusterName)
	}

	key := meta.User
	if key == "" {
		key = meta.ClientIP
	}
	healthy := func(n *lb.Node) bool {
		return t.health.Healthy(Key(tenantName, matched.service.cfg.Name, clusterName, n.Addr))
	}
	node, err := ct.balancer.Pick(key, healthy)
	if err != nil {
		return nil, fmt.Errorf("%w: %s/%s", ErrNoHealthyInstance, matched.service.cfg.Name, clusterName)
	}

	return &Resolution{
		Tenant:     tenantName,
		Service:    matched.service.cfg.Name,
		Cluster:    clusterName,
		Instance:   node.Addr,
		Addr:       node.Addr,
		RouteID:    matched.cfg.ID,
		Version:    ct.cfg.Version,
		Protocol:   matched.service.cfg.Protocol,
		Route:      matched.cfg,
		ClusterCfg: ct.cfg,
		Rewrite:    matched.cfg.Rewrite,
	}, nil
}

// Service 按名字返回服务配置，供管理接口使用。
func (t *Table) Service(tenant, service string) (*config.Service, bool) {
	tt, ok := t.tenants[tenant]
	if !ok {
		return nil, false
	}
	st, ok := tt.services[service]
	if !ok {
		return nil, false
	}
	return st.cfg, true
}

// RewritePath 应用路由上的路径改写规则。
func RewritePath(path string, rw *config.Rewrite) string {
	if rw == nil {
		return path
	}
	if rw.StripPrefix != "" && strings.HasPrefix(path, rw.StripPrefix) {
		path = strings.TrimPrefix(path, rw.StripPrefix)
		if path == "" {
			path = "/"
		}
	}
	if rw.Prefix != "" {
		path = strings.TrimSuffix(rw.Prefix, "/") + "/" + strings.TrimPrefix(path, "/")
	}
	return path
}
