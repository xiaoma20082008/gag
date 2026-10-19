package discovery

import (
	"context"
	"errors"
	"net"
	"sync"

	"gag/internal/config"
	"gag/internal/core"
	"gag/internal/lb"
	gerr "gag/pkg/errors"
)

// Resolver 在调用期把请求解析到具体实例：集群分流 → 取实例 → 负载均衡。
type Resolver struct {
	client DiscoveryClient

	mu        sync.RWMutex
	balancers map[string]lb.LoadBalancer
}

func NewResolver(client DiscoveryClient) *Resolver {
	return &Resolver{client: client, balancers: map[string]lb.LoadBalancer{}}
}

// Resolve 返回本次尝试的目标实例、集群名与版本。
func (r *Resolver) Resolve(ctx context.Context, c *core.RequestContext) (*core.Instance, string, string, error) {
	d := c.Decision()
	if d == nil {
		return nil, "", "", gerr.New(gerr.CodeInternal, "缺少路由决策")
	}

	cluster := core.SelectCluster(d.Route, metaOf(c))
	if cluster == "" {
		return nil, "", "", gerr.Errorf(gerr.CodeBadGateway, "路由 %s 未指定集群", d.RouteID).
			WithCause(core.ErrClusterNotFound)
	}

	insts, err := r.client.DiscoverInstances(ctx, d.Tenant, d.Service, cluster)
	if err != nil {
		// 注册中心不可达是基础设施故障，与"没有实例"分开报。
		return nil, "", "", gerr.New(gerr.CodeBadGateway, "实例来源查询失败").
			WithCause(errors.Join(core.ErrInstanceSource, err))
	}
	if len(insts) == 0 {
		return nil, "", "", gerr.Errorf(gerr.CodeUpstreamUnavailable,
			"集群 %s 没有可用实例", cluster).WithCause(core.ErrNoInstances)
	}

	clusterCfg := clusterOf(d.ServiceCfg, cluster)
	kind, version := "", ""
	if clusterCfg != nil {
		kind, version = clusterCfg.LB, clusterCfg.Version
	}

	balancer, err := r.balancerFor(d.Tenant, d.Service, cluster, kind)
	if err != nil {
		return nil, "", "", gerr.New(gerr.CodeInternal, "负载均衡策略非法").WithCause(err)
	}

	inst, err := balancer.Select(lb.WithHashKey(ctx, hashKey(c)), insts)
	if err != nil {
		return nil, "", "", gerr.Errorf(gerr.CodeUpstreamUnavailable,
			"集群 %s 没有可用实例", cluster).WithCause(err)
	}
	return inst, cluster, version, nil
}

// balancerFor 返回集群对应的负载均衡器，按 租户/服务/集群 缓存。
func (r *Resolver) balancerFor(tenant, service, cluster, kind string) (lb.LoadBalancer, error) {
	key := tenant + "/" + service + "/" + cluster

	r.mu.RLock()
	b, ok := r.balancers[key]
	r.mu.RUnlock()
	if ok {
		return b, nil
	}

	b, err := lb.New(kind)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if exist, ok := r.balancers[key]; ok {
		return exist, nil
	}
	r.balancers[key] = b
	return b, nil
}

// metaOf 把请求上下文转成查找链需要的特征。
//
// User 取认证后的身份：分流与一致性哈希在调用期执行，此时认证已经完成。
func metaOf(c *core.RequestContext) *core.RequestMeta {
	meta := &core.RequestMeta{
		Tenant:   c.Tenant,
		Method:   c.Request.Method,
		Path:     c.Request.URL.Path,
		Header:   c.Request.Header,
		Query:    c.Request.URL.Query(),
		ClientIP: c.ClientIP,
		User:     c.Subject,
	}
	if c.Request.Host != "" {
		meta.Host = hostOnly(c.Request.Host)
	}
	return meta
}

// hashKey 选一致性哈希的稳定标识：登录用户优先，未认证退化为客户端 IP。
func hashKey(c *core.RequestContext) string {
	if c.Subject != "" {
		return c.Subject
	}
	return c.ClientIP
}

// clusterOf 在服务配置里找集群声明；注册中心动态发现的集群不在配置里。
func clusterOf(svc *config.Service, name string) *config.Cluster {
	if svc == nil {
		return nil
	}
	for i := range svc.Clusters {
		if svc.Clusters[i].Name == name {
			return &svc.Clusters[i]
		}
	}
	return nil
}

func hostOnly(hostport string) string {
	if hostport == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}
