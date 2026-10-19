package discovery

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"gag/internal/config"
	"gag/internal/core"
)

type StaticDiscoveryClient struct {
	byKey map[string][]*core.Instance
}

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

func (c *StaticDiscoveryClient) Warm(context.Context, string, string, string) error { return nil }

func (c *StaticDiscoveryClient) Name() string { return "static" }

func (c *StaticDiscoveryClient) Close() error { return nil }

func instanceKey(tenant, service, cluster string) string {
	return tenant + "/" + service + "/" + cluster
}

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
