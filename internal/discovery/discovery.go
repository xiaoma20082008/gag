package discovery

import (
	"context"
	"fmt"
	"io"
	"sync"

	"gag/internal/config"
	"gag/internal/core"
)

// DiscoveryClient 是实例来源的统一抽象。
type DiscoveryClient interface {
	Name() string
	DiscoverInstances(ctx context.Context, tenant, service, cluster string) ([]*core.Instance, error)
	io.Closer
}

// Warmer 由支持预热的实现提供：装配期提前查一遍，把实例缓存焐热。
type Warmer interface {
	Warm(ctx context.Context, tenant, service, cluster string) error
}

func NewDiscoveryClient(cfg *config.DiscoveryClientConfig) (DiscoveryClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("discovery: 配置为 nil")
	}
	switch cfg.Type {
	case "", "static":
		return NewStaticDiscoveryClient(cfg), nil
	case "nacos":
		return newCachedNacos(cfg)
	case "consul":
		return nil, fmt.Errorf("discovery: consul 适配器尚未实现")
	default:
		return nil, fmt.Errorf("discovery: 未知的实例来源类型 %q", cfg.Type)
	}
}

var (
	nacosMu      sync.Mutex
	nacosClients = map[string]*NacosDiscoveryClient{}
)

func newCachedNacos(cfg *config.DiscoveryClientConfig) (DiscoveryClient, error) {
	key := fmt.Sprintf("%s|%d|%s|%s|%s|%d|%s",
		cfg.Host, cfg.Port, cfg.Namespace, cfg.Protocol, cfg.RootPath, cfg.TimeoutMs, cfg.LogLevel)

	nacosMu.Lock()
	defer nacosMu.Unlock()
	if c, ok := nacosClients[key]; ok {
		return c, nil
	}
	c, err := NewNacosDiscoveryClient(cfg)
	if err != nil {
		return nil, err
	}
	nacosClients[key] = c
	return c, nil
}

func ResetClientCache() {
	nacosMu.Lock()
	defer nacosMu.Unlock()
	nacosClients = map[string]*NacosDiscoveryClient{}
}
