package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultListen 是未显式配置监听地址时的默认值。
const DefaultListen = ":8080"

// DefaultAdminListen 是未显式配置管理端口时的默认值。
//
// 默认只绑回环：管理面暴露配置下发与回滚能力，不应随业务端口一起对外。
const DefaultAdminListen = "127.0.0.1:9090"

// LoadFile 从文件加载配置。
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s: %w", path, err)
	}
	cfg, err := Load(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	return cfg, nil
}

// Load 解析 YAML 配置，应用默认值并做完整校验。
//
// 使用 strict 解码：拼错的字段会直接报错，避免"配置静默不生效"。
func Load(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("配置为空")
		}
		return nil, err
	}

	ApplyDefaults(&cfg)
	if err := Validate(&cfg); err != nil {
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
				for ii := range cl.Instances {
					if cl.Instances[ii].Weight <= 0 {
						cl.Instances[ii].Weight = 1
					}
				}
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
				for si := range r.Split {
					if r.Split[si].Weight == 0 && r.Split[si].Match == nil {
						r.Split[si].Weight = 1
					}
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

// 保证 Duration 常量在编译期可见，避免魔法数字散落。
const (
	second = Duration(1e9)
)
