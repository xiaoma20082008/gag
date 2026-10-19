package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Snapshot 是不可变配置快照。
//
// 数据面以 Snapshot 为最小切换单位：任何一次下发都整体替换，
// 不会出现新旧配置交叉生效的中间态（对应设计文档 §7.3 的原子替换要求）。
type Snapshot struct {
	cfg       *Config
	version   string
	hash      string
	createdAt time.Time
}

// NewSnapshot 校验并冻结一份配置。
func NewSnapshot(cfg *Config) (*Snapshot, error) {
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	hash, err := hashConfig(cfg)
	if err != nil {
		return nil, err
	}
	version := cfg.Version
	if version == "" {
		version = hash[:12]
	}
	return &Snapshot{
		cfg:       cfg,
		version:   version,
		hash:      hash,
		createdAt: time.Now().UTC(),
	}, nil
}

// Config 返回只读配置视图。调用方不得修改返回的结构体。
func (s *Snapshot) Config() *Config {
	if s == nil {
		return nil
	}
	return s.cfg
}

func (s *Snapshot) Version() string { return s.version }

// Hash 是配置内容的 sha256，用于判断配置是否真正发生变化。
func (s *Snapshot) Hash() string { return s.hash }

func (s *Snapshot) CreatedAt() time.Time { return s.createdAt }

// Tenant 按名字查找租户。
func (s *Snapshot) Tenant(name string) (*Tenant, bool) {
	if s == nil || s.cfg == nil {
		return nil, false
	}
	for i := range s.cfg.Tenants {
		if s.cfg.Tenants[i].Name == name {
			return &s.cfg.Tenants[i], true
		}
	}
	return nil, false
}

// DefaultTenant 返回未显式指定租户时使用的租户名。
func (s *Snapshot) DefaultTenant() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	if s.cfg.Server.DefaultTenant != "" {
		return s.cfg.Server.DefaultTenant
	}
	if len(s.cfg.Tenants) > 0 {
		return s.cfg.Tenants[0].Name
	}
	return ""
}

// Service 在指定租户内查找服务。
func (s *Snapshot) Service(tenant, service string) (*Service, bool) {
	t, ok := s.Tenant(tenant)
	if !ok {
		return nil, false
	}
	for i := range t.Services {
		if t.Services[i].Name == service {
			return &t.Services[i], true
		}
	}
	return nil, false
}

// String 便于日志输出。
func (s *Snapshot) String() string {
	if s == nil {
		return "<nil>"
	}
	return fmt.Sprintf("snapshot{version=%s hash=%s}", s.version, s.hash[:8])
}

func hashConfig(cfg *Config) (string, error) {
	// 通过 JSON 归一化结构体，避免 map 迭代顺序导致的哈希漂移。
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("配置哈希失败: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
