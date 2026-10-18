package discovery

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"gag/internal/config"
	"gag/internal/core"
)

// StaticDiscoveryClient 从配置里写死的清单提供实例。
//
// 用于本地开发与单机部署：没有注册中心时也要能把链路跑通。
// 实例自带 tenant/service/cluster 三跳定位，查找时按三元组过滤——
// 不能返回全部实例，否则负载均衡会把流量打到别的服务上。
type StaticDiscoveryClient struct {
	byKey map[string][]*core.Instance
}

// NewStaticDiscoveryClient 编译静态实例清单。
func NewStaticDiscoveryClient(cfg *config.DiscoveryClientConfig) *StaticDiscoveryClient {
	c := &StaticDiscoveryClient{byKey: map[string][]*core.Instance{}}
	if cfg == nil {
		return c
	}
	for _, ins := range cfg.Instances {
		inst, ok := newInstance(ins.Addr, ins.Weight)
		if !ok {
			continue
		}
		key := instanceKey(ins.Tenant, ins.Service, ins.Cluster)
		c.byKey[key] = append(c.byKey[key], inst)
	}
	return c
}

func (c *StaticDiscoveryClient) DiscoverInstances(_ context.Context, tenant, service, cluster string) ([]*core.Instance, error) {
	return c.byKey[instanceKey(tenant, service, cluster)], nil
}

// Warm 对静态来源是空操作：查的是内存，没有冷启动问题。
func (c *StaticDiscoveryClient) Warm(context.Context, string, string, string) error { return nil }

func instanceKey(tenant, service, cluster string) string {
	return tenant + "/" + service + "/" + cluster
}

// newInstance 把 host:port 拆成 Instance；地址非法时跳过而不是 panic，
// 配置校验已经拦过一次，这里只是防御性处理。
func newInstance(addr string, weight int) (*core.Instance, bool) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, false
	}
	if weight <= 0 {
		weight = 1
	}
	return &core.Instance{
		ID:      addr,
		Host:    host,
		Port:    port,
		Address: fmt.Sprintf("%s:%d", host, port),
		Weight:  weight,
	}, true
}

var (
	_ DiscoveryClient = (*StaticDiscoveryClient)(nil)
	_ Warmer          = (*StaticDiscoveryClient)(nil)
)
