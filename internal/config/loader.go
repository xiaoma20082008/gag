package config

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Format string

const (
	FormatYAML Format = "yaml"
	FormatJSON Format = "json"
)

type ConfigLoader interface {
	Load(path string) (*Config, error)
}

type Decoder interface {
	Decode(r io.Reader) (*Config, error)
}

type YamlLoader struct{}
type JsonLoader struct{}

func (l *YamlLoader) Load(path string) (*Config, error) { return loadFile(path, l.Decode) }

func (l *JsonLoader) Load(path string) (*Config, error) { return loadFile(path, l.Decode) }

func (l *YamlLoader) Decode(r io.Reader) (*Config, error) { return decode(r, parseYAML) }

func (l *JsonLoader) Decode(r io.Reader) (*Config, error) { return decode(r, parseJSON) }

// NewLoader 按扩展名选择 loader：.yaml/.yml → YAML，.json → JSON。
func NewLoader(path string) (ConfigLoader, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		return &YamlLoader{}, nil
	case ".json":
		return &JsonLoader{}, nil
	case "":
		return nil, fmt.Errorf("配置文件 %q 没有扩展名，无法判断格式（支持 .yaml/.yml/.json）", path)
	default:
		return nil, fmt.Errorf("配置文件 %q 的格式 %q 不受支持（支持 .yaml/.yml/.json）", path, ext)
	}
}

func LoaderFor(format Format) (ConfigLoader, error) {
	switch Format(strings.ToLower(strings.TrimSpace(string(format)))) {
	case FormatYAML, "":
		return &YamlLoader{}, nil
	case FormatJSON:
		return &JsonLoader{}, nil
	default:
		return nil, fmt.Errorf("不支持的配置格式 %q（支持 yaml/json）", format)
	}
}

func LoadFile(path string) (*Config, error) {
	loader, err := NewLoader(path)
	if err != nil {
		return nil, err
	}
	return loader.Load(path)
}

// Load 从 reader 解析 YAML 配置。
//
// 保留这个函数是因为绝大多数调用方（测试、控制面 body）手里只有字节流：
// 它等价于 (&YamlLoader{}).Decode(r)，不构成第二条解析路径。
func Load(r io.Reader) (*Config, error) { return (&YamlLoader{}).Decode(r) }

// parse 是格式相关的解析步骤：只负责把字节变成 Config，不碰默认值与校验。
type parse func(io.Reader) (*Config, error)

// decode 是唯一的流水线：解析 → 默认值 → 校验。
func decode(r io.Reader, p parse) (*Config, error) {
	cfg, err := p(r)
	if err != nil {
		return nil, err
	}
	ApplyDefaults(cfg)
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadFile 打开文件并走同一条流水线；错误里带上路径，
// 因为调用方手里通常只有一个路径，没有更多上下文。
func loadFile(path string, p parse) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s: %w", path, err)
	}
	defer f.Close()

	cfg, err := decode(f, p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// parseYAML 用 strict 解码：拼错的字段会直接报错，避免"配置静默不生效"。
func parseYAML(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("配置为空")
		}
		return nil, err
	}
	return &cfg, nil
}

// parseJSON 同样拒绝未知字段，与 YAML 的 KnownFields(true) 对齐。
func parseJSON(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("配置为空")
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ApplyDefaults 为可选字段填充默认值。
func ApplyDefaults(c *Config) {
	if c.Server.Listen == "" {
		c.Server.Listen = DefaultListen
	}
	if c.Server.AdminListen == "" {
		c.Server.AdminListen = DefaultAdminListen
	}
	if c.Server.ReadHeaderTimeout == 0 {
		c.Server.ReadHeaderTimeout = Duration(10e9) // 10s
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = Duration(90e9) // 90s
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = Duration(15e9) // 15s
	}
	if c.Server.MaxBodyBytes <= 0 {
		c.Server.MaxBodyBytes = 1 << 20 // 1MiB
	}
	if c.Server.CORS != nil {
		if len(c.Server.CORS.AllowMethods) == 0 {
			c.Server.CORS.AllowMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
		}
		if c.Server.CORS.MaxAge == 0 {
			c.Server.CORS.MaxAge = Duration(600e9)
		}
	}

	applyDiscoveryDefaults(c)

	for ti := range c.Tenants {
		t := &c.Tenants[ti]
		for si := range t.Services {
			s := &t.Services[si]
			if s.Protocol == "" {
				s.Protocol = "http"
			}
			if s.Timeout == 0 {
				s.Timeout = Duration(5e9)
			}
			applyPolicyDefaults(s)
			for ci := range s.Clusters {
				cl := &s.Clusters[ci]
				if cl.LB == "" {
					cl.LB = "round_robin"
				}
				if cl.HealthCheck == nil {
					cl.HealthCheck = &HealthCheckPolicy{}
				}
				applyHealthDefaults(cl.HealthCheck)
			}
			for ri := range s.Routes {
				r := &s.Routes[ri]
				if r.ID == "" {
					r.ID = fmt.Sprintf("%s.%s", s.Name, strings.TrimPrefix(r.Path, "/"))
				}
				if r.Timeout == 0 {
					r.Timeout = s.Timeout
				}
				applyAuthDefaults(r.Auth)
				if r.RateLimit != nil {
					applyRateLimitDefaults(r.RateLimit)
				}
				if r.Breaker != nil {
					applyBreakerDefaults(r.Breaker)
				}
				if r.Retry != nil {
					applyRetryDefaults(r.Retry)
				}
			}
		}
	}
}

func applyPolicyDefaults(s *Service) {
	if s.RateLimit != nil {
		applyRateLimitDefaults(s.RateLimit)
	}
	if s.Breaker != nil {
		applyBreakerDefaults(s.Breaker)
	}
	if s.Retry != nil {
		applyRetryDefaults(s.Retry)
	}
	applyAuthDefaults(s.Auth)
}

func applyAuthDefaults(p *AuthPolicy) {
	if p == nil || p.Header != "" {
		return
	}
	switch p.Type {
	case "api_key":
		p.Header = "X-API-Key"
	case "jwt":
		p.Header = "Authorization"
	}
}

func applyRateLimitDefaults(p *RateLimitPolicy) {
	if p.Burst <= 0 {
		p.Burst = int(p.RPS)
		if p.Burst < 1 {
			p.Burst = 1
		}
	}
	if p.Key == "" {
		p.Key = "route"
	}
	if p.Algorithm == "" {
		p.Algorithm = "token_bucket"
	}
	if p.Algorithm == "sliding_window" && p.Window == 0 {
		p.Window = Duration(1e9)
	}
}

func applyBreakerDefaults(p *BreakerPolicy) {
	if p.ErrorRate <= 0 || p.ErrorRate > 1 {
		p.ErrorRate = 0.5
	}
	if p.MinRequests <= 0 {
		p.MinRequests = 20
	}
	if p.OpenTimeout == 0 {
		p.OpenTimeout = Duration(5e9)
	}
	if p.HalfOpenRequests <= 0 {
		p.HalfOpenRequests = 3
	}
}

func applyRetryDefaults(p *RetryPolicy) {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 1
	}
	if p.Backoff == 0 {
		p.Backoff = Duration(50e6)
	}
	if p.MaxBackoff == 0 {
		p.MaxBackoff = Duration(1e9)
	}
	if p.Jitter < 0 || p.Jitter > 1 {
		p.Jitter = 0.2
	}
}

func applyHealthDefaults(p *HealthCheckPolicy) {
	if p.Path == "" {
		p.Path = "/healthz"
	}
	if p.Interval == 0 {
		p.Interval = Duration(5e9)
	}
	if p.Timeout == 0 {
		p.Timeout = Duration(2e9)
	}
	if p.FailureThreshold <= 0 {
		p.FailureThreshold = 3
	}
	if p.SuccessThreshold <= 0 {
		p.SuccessThreshold = 2
	}
}

func applyDiscoveryDefaults(c *Config) {
	if c.Server.Discovery == nil {
		c.Server.Discovery = &DiscoveryClientConfig{Type: "static"}
	}
	d := c.Server.Discovery
	if d.Type == "" {
		d.Type = "static"
	}
	if d.Type == "nacos" {
		if d.Protocol == "" {
			d.Protocol = "http"
		}
		if d.LogLevel == "" {
			d.LogLevel = "warn"
		}
		if d.TimeoutMs <= 0 {
			d.TimeoutMs = 5000
		}
	}
	for i := range d.Instances {
		if d.Instances[i].Weight <= 0 {
			d.Instances[i].Weight = 1
		}
	}
}
