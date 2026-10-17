// Package config 定义网关的声明式配置模型（DSL）。
//
// 配置是控制面与数据面之间唯一的契约：控制面负责生成、校验与版本化，
// 数据面只消费已经校验过的不可变快照（Snapshot）。
package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 让 YAML 中可以直接书写 "5s" / "200ms" 这类可读时长。
type Duration time.Duration

// Std 返回标准库时长，便于传给 net/http 等接口。
func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration 必须是字符串: %w", err)
	}
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("非法时长 %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Config 是网关的顶层配置。
type Config struct {
	// Version 是配置的语义版本号，由使用方维护；控制面另外维护自增修订号。
	Version string   `yaml:"version"`
	Server  Server   `yaml:"server"`
	Tenants []Tenant `yaml:"tenants"`
}

// Server 描述进程级参数。
//
// 进程会监听两个端口：Listen 承载业务流量（Gag Server），
// AdminListen 承载运维与控制面接口（Gag Admin）。
// 两者分离，使管理面可以只绑内网/回环，且管理端点的慢查询不会挤占业务连接。
type Server struct {
	Listen string `yaml:"listen"`
	// AdminListen 是管理端口；默认只绑回环地址。
	AdminListen       string   `yaml:"admin_listen"`
	AdminToken        string   `yaml:"admin_token"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	IdleTimeout       Duration `yaml:"idle_timeout"`
	ShutdownTimeout   Duration `yaml:"shutdown_timeout"`
	MaxBodyBytes      int64    `yaml:"max_body_bytes"`
	// DefaultTenant 在请求未携带租户标识时使用；为空时取第一个租户。
	DefaultTenant string   `yaml:"default_tenant"`
	IPAllow       []string `yaml:"ip_allow"`
	IPDeny        []string `yaml:"ip_deny"`
	CORS          *CORS    `yaml:"cors"`
}

// CORS 是入口跨域策略。
type CORS struct {
	AllowOrigins     []string `yaml:"allow_origins"`
	AllowMethods     []string `yaml:"allow_methods"`
	AllowHeaders     []string `yaml:"allow_headers"`
	ExposeHeaders    []string `yaml:"expose_headers"`
	AllowCredentials bool     `yaml:"allow_credentials"`
	MaxAge           Duration `yaml:"max_age"`
}

// Tenant 是路由与策略的隔离边界。
type Tenant struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Services    []Service `yaml:"services"`
}

// Service 是逻辑服务，承载协议、超时与治理策略。
type Service struct {
	Name     string   `yaml:"name"`
	Protocol string   `yaml:"protocol"` // http | grpc | ai
	Timeout  Duration `yaml:"timeout"`

	Routes   []Route   `yaml:"routes"`
	Clusters []Cluster `yaml:"clusters"`

	// 以下策略可被 Route 级别的同名字段整体覆盖。
	Auth      *AuthPolicy      `yaml:"auth"`
	Validate  *ValidatePolicy  `yaml:"validate"`
	RateLimit *RateLimitPolicy `yaml:"rate_limit"`
	Breaker   *BreakerPolicy   `yaml:"breaker"`
	Retry     *RetryPolicy     `yaml:"retry"`
	Headers   *HeaderPolicy    `yaml:"headers"`
}

// Route 是租户空间内的一条匹配规则。
type Route struct {
	ID      string            `yaml:"id"`
	Path    string            `yaml:"path"` // /v1/orders 或 /v1/orders/*（前缀匹配）
	Methods []string          `yaml:"methods"`
	Host    string            `yaml:"host"`
	Headers map[string]string `yaml:"headers"`
	Query   map[string]string `yaml:"query"`

	// Cluster 是默认目标集群；Split 非空时优先按分流规则选择。
	Cluster string      `yaml:"cluster"`
	Split   []SplitRule `yaml:"split"`
	Rewrite *Rewrite    `yaml:"rewrite"`
	Timeout Duration    `yaml:"timeout"`

	Auth      *AuthPolicy      `yaml:"auth"`
	Validate  *ValidatePolicy  `yaml:"validate"`
	RateLimit *RateLimitPolicy `yaml:"rate_limit"`
	Breaker   *BreakerPolicy   `yaml:"breaker"`
	Retry     *RetryPolicy     `yaml:"retry"`
}

// SplitRule 描述灰度分流：命中 Match 时使用 Cluster，否则按 Weight 分配。
type SplitRule struct {
	Cluster string      `yaml:"cluster"`
	Weight  int         `yaml:"weight"`
	Match   *SplitMatch `yaml:"match"`
}

// SplitMatch 是分流条件，多个字段之间是 AND 关系。
type SplitMatch struct {
	Headers    map[string]string `yaml:"headers"`
	Cookies    map[string]string `yaml:"cookies"`
	Users      []string          `yaml:"users"`
	Regions    []string          `yaml:"regions"`
	Percentage float64           `yaml:"percentage"` // 0-100，按 user/client_ip 稳定哈希
}

// Rewrite 在转发前改写路径。
type Rewrite struct {
	StripPrefix string `yaml:"strip_prefix"`
	Prefix      string `yaml:"prefix"`
}

// Cluster 是部署单元，负责地域、版本与实例池。
type Cluster struct {
	Name      string            `yaml:"name"`
	Region    string            `yaml:"region"`
	Version   string            `yaml:"version"`
	LB        string            `yaml:"lb"` // round_robin | random | least_conn | consistent_hash
	Instances []Instance        `yaml:"instances"`
	Metadata  map[string]string `yaml:"metadata"`

	HealthCheck *HealthCheckPolicy `yaml:"health_check"`
}

// Instance 是最终的执行节点。
type Instance struct {
	Addr     string            `yaml:"addr"` // host:port
	Weight   int               `yaml:"weight"`
	Metadata map[string]string `yaml:"metadata"`
}

// HealthCheckPolicy 描述主动健康检查参数。
type HealthCheckPolicy struct {
	Path             string   `yaml:"path"`
	Interval         Duration `yaml:"interval"`
	Timeout          Duration `yaml:"timeout"`
	FailureThreshold int      `yaml:"failure_threshold"`
	SuccessThreshold int      `yaml:"success_threshold"`
}

// AuthPolicy 描述认证方式。
type AuthPolicy struct {
	Type        string   `yaml:"type"` // none | api_key | jwt
	Header      string   `yaml:"header"`
	Keys        []string `yaml:"keys"`   // api_key
	Secret      string   `yaml:"secret"` // jwt HS256
	Issuer      string   `yaml:"issuer"`
	Audience    string   `yaml:"audience"`
	TenantClaim string   `yaml:"tenant_claim"`
	Scopes      []string `yaml:"scopes"`
}

// ValidatePolicy 描述请求参数校验。
type ValidatePolicy struct {
	MaxBodyBytes       int64    `yaml:"max_body_bytes"`
	ContentTypes       []string `yaml:"content_types"`
	RequiredHeaders    []string `yaml:"required_headers"`
	RequiredQuery      []string `yaml:"required_query"`
	RequiredBodyFields []string `yaml:"required_body_fields"`
}

// RateLimitPolicy 描述限流。
type RateLimitPolicy struct {
	RPS       float64  `yaml:"rps"`
	Burst     int      `yaml:"burst"`
	Key       string   `yaml:"key"`       // ip | tenant | route | user
	Algorithm string   `yaml:"algorithm"` // token_bucket | sliding_window
	Window    Duration `yaml:"window"`
}

// BreakerPolicy 描述熔断。
type BreakerPolicy struct {
	ErrorRate        float64  `yaml:"error_rate"` // 0-1
	MinRequests      int      `yaml:"min_requests"`
	OpenTimeout      Duration `yaml:"open_timeout"`
	HalfOpenRequests int      `yaml:"half_open_requests"`
}

// RetryPolicy 描述重试。
type RetryPolicy struct {
	MaxAttempts int      `yaml:"max_attempts"`
	Backoff     Duration `yaml:"backoff"`
	MaxBackoff  Duration `yaml:"max_backoff"`
	Jitter      float64  `yaml:"jitter"`   // 0-1
	RetryOn     []int    `yaml:"retry_on"` // 额外视为可重试的下游状态码
}

// HeaderPolicy 描述请求/响应头改写。
type HeaderPolicy struct {
	RequestAdd     map[string]string `yaml:"request_add"`
	RequestRemove  []string          `yaml:"request_remove"`
	ResponseAdd    map[string]string `yaml:"response_add"`
	ResponseRemove []string          `yaml:"response_remove"`
}
