package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type ConfigClient interface {
	// Type 返回来源类型，用于日志与错误信息。
	Type() string

	// Get 返回 key 对应的配置文档原文；读不到时返回 def。
	//
	// 注意：返回 def 意味着调用方无法区分"内容就是 def"和"读失败"。
	// 因此读配置时 def 一律传空串，空串即视为失败（见 LoadFrom）。
	Get(ctx context.Context, key, def string) string

	// OnChange 订阅 key 的变更，内容变化时回调 f。
	OnChange(ctx context.Context, key string, f func(newVal string))

	// Close 释放客户端持有的资源（连接、轮询 goroutine）。
	Close() error
}

func NewConfigClient(ccc *ConfigClientConfig) (ConfigClient, error) {
	var configClient ConfigClient
	var err error
	switch ccc.Type {
	case "file":
		configClient, err = NewFileConfigClient(ccc)
	case "nacos":
		configClient, err = NewNacosConfigClient(ccc)
	default:
		return nil, fmt.Errorf("unknown config type: '%s'", ccc.Type)
	}
	if err != nil {
		return nil, err
	}
	return configClient, nil
}

// ErrUnsupportedFormat 表示配置格式不受支持。
//
// 与"内容非法"区分开：控制面据此回 415 而不是 400——
// 客户端发错了 Content-Type 和服务端解析失败是两类问题。
var ErrUnsupportedFormat = errors.New("不支持的配置格式")

// FormatOf 按文档名推断格式。
func FormatOf(key string) (Format, error) {
	ext := strings.ToLower(filepath.Ext(key))
	switch ext {
	case ".yaml", ".yml":
		return FormatYAML, nil
	case ".json":
		return FormatJSON, nil
	case "":
		return "", fmt.Errorf("%w: %q 没有扩展名，无法判断（支持 .yaml/.yml/.json）", ErrUnsupportedFormat, key)
	default:
		return "", fmt.Errorf("%w: %q 的扩展名 %q（支持 .yaml/.yml/.json）", ErrUnsupportedFormat, key, ext)
	}
}

// parserFor 返回格式对应的解析步骤。
func parserFor(format Format) (parse, error) {
	switch Format(strings.ToLower(strings.TrimSpace(string(format)))) {
	case FormatYAML, "":
		return parseYAML, nil
	case FormatJSON:
		return parseJSON, nil
	default:
		return nil, fmt.Errorf("%w: %q（支持 yaml/json）", ErrUnsupportedFormat, format)
	}
}

// ParseDocument 解析一份内存中的配置文档。
//
// 与从磁盘读文件走完全相同的流水线（解析 → 默认值 → 校验），
// 控制面下发的 body 因此不会走上一条更宽松的路径。
func ParseDocument(data []byte, format Format) (*Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("配置为空")
	}
	p, err := parserFor(format)
	if err != nil {
		return nil, err
	}
	return decode(bytes.NewReader(data), p)
}

// LoadFrom 从客户端取文档并冻结成快照。
//
// 这是"来源"到"快照"的唯一入口：网关启动与热加载都走它，
// 保证第一次加载和后续变更的校验强度一致。
func LoadFrom(ctx context.Context, client ConfigClient, key string, format Format) (*Snapshot, error) {
	if client == nil {
		return nil, errors.New("config: client 为 nil")
	}

	// def 传空串：空内容视为失败，而不是"合法但为空的配置"。
	raw := client.Get(ctx, key, "")
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("config: %s 客户端没有返回内容（key=%q）", client.Type(), key)
	}
	return SnapshotFrom([]byte(raw), key, format)
}

// SnapshotFrom 解析一份文档并冻结成快照。
//
// 热加载路径用它：OnChange 给的是原文，不需要再回头问客户端要一遍。
func SnapshotFrom(data []byte, key string, format Format) (*Snapshot, error) {
	if format == "" {
		// 没有显式指定时按名字推断；推断不出来（例如 nacos 的 dataId
		// 叫 "gateway"）就按 YAML 处理——JSON 是 YAML 1.2 的子集，
		// 两种写法都能解析，不会因为少了扩展名就拒绝启动。
		if guessed, err := FormatOf(key); err == nil {
			format = guessed
		} else {
			format = FormatYAML
		}
	}

	cfg, err := ParseDocument(data, format)
	if err != nil {
		return nil, fmt.Errorf("config: 解析 %q: %w", key, err)
	}
	return NewSnapshot(cfg)
}
