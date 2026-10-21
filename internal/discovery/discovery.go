package discovery

import (
	"context"
	"fmt"
	"io"

	"gag/internal/config"
	"gag/internal/core"
)

// DiscoveryClient 服务发现客户端。
type DiscoveryClient interface {
	Name() string
	DiscoverInstances(ctx context.Context, tenant, service, cluster string) ([]*core.Instance, error)
	io.Closer
}

// Warmer 预热。
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
		return NewNacosDiscoveryClient(cfg)
	case "consul":
		return nil, fmt.Errorf("discovery: consul 适配器尚未实现")
	default:
		return nil, fmt.Errorf("discovery: 未知的实例来源类型 %q", cfg.Type)
	}
}
