package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

const defaultNacosGroup = "DEFAULT_GROUP"

type NacosConfigClient struct {
	sdk    config_client.IConfigClient
	group  string
	dataID string
}

func (client *NacosConfigClient) Type() string { return "nacos" }

func (client *NacosConfigClient) Get(ctx context.Context, key, def string) string {
	param, err := client.param(key)
	if err != nil {
		return def
	}
	if err := ctx.Err(); err != nil {
		return def
	}

	val, err := client.sdk.GetConfig(param)
	if err != nil || strings.TrimSpace(val) == "" {
		return def
	}
	return val
}

func (client *NacosConfigClient) OnChange(ctx context.Context, key string, f func(newVal string)) {
	if f == nil {
		return
	}
	param, err := client.param(key)
	if err != nil {
		return
	}
	param.OnChange = func(_, _, _, data string) { f(data) }

	if err := client.sdk.ListenConfig(param); err != nil {
		return
	}

	// SDK 的监听是长连接，ctx 取消后必须主动退订，否则会一直持有连接
	// 并继续回调到已经停机的数据面上。
	go func() {
		<-ctx.Done()
		_ = client.sdk.CancelListenConfig(param)
	}()
}

func (client *NacosConfigClient) Close() error {
	client.sdk.CloseClient()
	return nil
}

func (client *NacosConfigClient) param(key string) (vo.ConfigParam, error) {
	dataID := strings.TrimSpace(key)
	if dataID == "" {
		dataID = client.dataID
	}
	if dataID == "" {
		return vo.ConfigParam{}, errors.New("config: nacos 缺少 data_id（key 与配置里的 data_id 都为空）")
	}
	return vo.ConfigParam{
		DataId: dataID,
		Group:  cmp.Or(client.group, defaultNacosGroup),
	}, nil
}

func NewNacosConfigClient(cfg *ConfigClientConfig) (*NacosConfigClient, error) {
	if cfg == nil {
		return nil, errors.New("config: nacos 客户端配置为 nil")
	}
	if cfg.Type != "" && cfg.Type != "nacos" {
		return nil, fmt.Errorf("config: nacos 客户端收到 type=%q", cfg.Type)
	}
	host, port, err := cfg.Endpoint()
	if err != nil {
		return nil, err
	}

	baseDir := filepath.Join(os.TempDir(), "gag", "nacos")

	server := []constant.ServerConfig{{
		IpAddr:      host,
		Port:        uint64(port),
		ContextPath: cmp.Or(cfg.RootPath, "/nacos"),
		Scheme:      cmp.Or(cfg.Protocol, "http"),
	}}

	clientCfg := constant.ClientConfig{
		NamespaceId:         cfg.Namespace, // public 为空
		TimeoutMs:           uint64(cfg.TimeoutMs),
		NotLoadCacheAtStart: true,
		LogDir:              filepath.Join(baseDir, "log"),
		CacheDir:            filepath.Join(baseDir, "cache"),
		LogLevel:            cmp.Or(cfg.LogLevel, "warn"),
	}
	if clientCfg.TimeoutMs == 0 {
		clientCfg.TimeoutMs = 5000
	}

	sdk, err := clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &clientCfg,
		ServerConfigs: server,
	})
	if err != nil {
		return nil, fmt.Errorf("config: 连接 nacos %s:%d: %w", host, port, err)
	}

	return &NacosConfigClient{
		sdk:    sdk,
		group:  cfg.Group,
		dataID: cfg.DataId,
	}, nil
}
