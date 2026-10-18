package discovery

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"gag/internal/config"
	"gag/internal/core"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

type NacosDiscoveryClient struct {
	sdk naming_client.INamingClient
	cfg *config.DiscoveryClientConfig
}

func (client *NacosDiscoveryClient) DiscoverInstances(ctx context.Context, tenant, service, cluster string) ([]*core.Instance, error) {
	instances, err := client.sdk.SelectInstances(vo.SelectInstancesParam{
		GroupName:   tenant,
		ServiceName: service,
		Clusters:    []string{cluster},
		HealthyOnly: true,
	})
	if err != nil {
		if len(instances) > 0 {
			return client.mapToInstances(instances), nil
		}
		// SDK 对"服务下没有实例"返回空切片 + error；这是容量问题，不是故障，
		// 交给调用期按"没有可用实例"处理（503），不要升级成 502。
		if isNoInstanceErr(err) {
			return nil, nil
		}
		return nil, err
	}
	return client.mapToInstances(instances), nil
}

func (client *NacosDiscoveryClient) mapToInstances(instances []model.Instance) []*core.Instance {
	res := make([]*core.Instance, len(instances))
	for i, inst := range instances {
		res[i] = client.mapToInstance(inst)
	}
	return res
}

func (client *NacosDiscoveryClient) mapToInstance(instance model.Instance) *core.Instance {
	return &core.Instance{
		ID:       instance.InstanceId,
		Host:     instance.Ip,
		Port:     int(instance.Port),
		Address:  fmt.Sprintf("%s:%d", instance.Ip, int(instance.Port)),
		Weight:   int(instance.Weight),
		Metadata: maps.Clone(instance.Metadata),
	}
}

func (client *NacosDiscoveryClient) Warm(ctx context.Context, tenant, service, cluster string) error {
	_, err := client.DiscoverInstances(ctx, tenant, service, cluster)
	return err
}

func (client *NacosDiscoveryClient) Name() string { return "nacos" }

func (client *NacosDiscoveryClient) Close() error {
	client.sdk.CloseClient()
	return nil
}

func isNoInstanceErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "instance list is empty")
}

func getNacosLogDir() string {
	return filepath.Join(os.TempDir(), "gag", "nacos", "log")
}

func getNacosCacheDir() string {
	return filepath.Join(os.TempDir(), "gag", "nacos", "cache")
}

const defaultTimeoutMs = 5000

func NewNacosDiscoveryClient(cfg *config.DiscoveryClientConfig) (*NacosDiscoveryClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("discovery: nacos 客户端配置为 nil")
	}
	// address 与 host/port 两种写法在这里统一，校验层用的是同一个方法。
	host, port, err := cfg.Endpoint()
	if err != nil {
		return nil, err
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = defaultTimeoutMs
	}

	protocol := cfg.Protocol
	if protocol == "" {
		protocol = "http"
	}
	cc := &constant.ClientConfig{
		NamespaceId: cfg.Namespace,
		OpenKMS:     false,
		TimeoutMs:   uint64(cfg.TimeoutMs),
		LogLevel:    cfg.LogLevel,
		CacheDir:    getNacosCacheDir(),
		LogDir:      getNacosLogDir(),
	}
	if cc.LogLevel == "" {
		cc.LogLevel = "info"
	}

	sc := *constant.NewServerConfig(host, uint64(port))
	sc.Scheme = protocol
	if cfg.RootPath != "" {
		sc.ContextPath = cfg.RootPath
	}

	if err := os.MkdirAll(cc.LogDir, 0755); err != nil {
		return nil, fmt.Errorf("discovery: 创建 nacos 日志目录 %s: %w", cc.LogDir, err)
	}

	sdk, err := clients.NewNamingClient(
		vo.NacosClientParam{
			ClientConfig:  cc,
			ServerConfigs: []constant.ServerConfig{sc},
		},
	)
	if err != nil {
		return nil, err
	}
	return &NacosDiscoveryClient{
		sdk: sdk,
		cfg: cfg,
	}, nil
}

var (
	_ DiscoveryClient = (*NacosDiscoveryClient)(nil)
	_ Warmer          = (*NacosDiscoveryClient)(nil)
)
