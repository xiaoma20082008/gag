package discovery

import (
	"context"
	"fmt"
	"sync"

	"gag/internal/config"
	"gag/internal/core"
)

// DiscoveryClient 是实例来源的统一抽象。
//
// 集群声明"有哪些集群"，实例回答"集群里现在有哪些节点"。后者是运行时数据：
// 静态场景写在配置里，动态场景由 nacos/consul 这类注册中心提供。
type DiscoveryClient interface {
	DiscoverInstances(ctx context.Context, tenant, service, cluster string) ([]*core.Instance, error)
}

// Warmer 由支持预热的实现提供：装配期提前查一遍，把实例缓存焐热。
//
// 不是所有来源都需要——静态来源查的是内存，没有冷启动问题。
type Warmer interface {
	Warm(ctx context.Context, tenant, service, cluster string) error
}

// NewDiscoveryClient 按配置构造实例来源。
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

// nacos 客户端按连接参数进程内复用
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

// ResetClientCache 清空客户端缓存，仅供测试使用。
func ResetClientCache() {
	nacosMu.Lock()
	defer nacosMu.Unlock()
	nacosClients = map[string]*NacosDiscoveryClient{}
}
