package config

import (
	"encoding/json/v2"
	"fmt"
	"net"
	"strconv"
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

// UnmarshalJSON 让 JSON 配置也能写 "5s" / "200ms"。
//
// 不接受裸数字：YAML 里 Duration 是字符串，两种格式的写法必须一致，
// 否则同一份配置换个格式就悄悄变了语义（纳秒 vs 秒）。
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
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

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// Config 是网关的顶层配置。
type Config struct {
	// Version 是配置的语义版本号，由使用方维护；控制面另外维护自增修订号。
	Version string   `yaml:"version" json:"version"`
	Server  Server   `yaml:"server" json:"server"`
	Tenants []Tenant `yaml:"tenants" json:"tenants"`
}

// Server 描述进程级参数。
//
// 进程会监听两个端口：Listen 承载业务流量（Gag Server），
// AdminListen 承载运维与控制面接口（Gag Admin）。
// 两者分离，使管理面可以只绑内网/回环，且管理端点的慢查询不会挤占业务连接。
type Server struct {
	Listen string `yaml:"listen" json:"listen"`
	// AdminListen 是管理端口；默认只绑回环地址。
	AdminListen       string   `yaml:"admin_listen" json:"admin_listen"`
	AdminToken        string   `yaml:"admin_token" json:"admin_token"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout" json:"read_header_timeout"`
	IdleTimeout       Duration `yaml:"idle_timeout" json:"idle_timeout"`
	ShutdownTimeout   Duration `yaml:"shutdown_timeout" json:"shutdown_timeout"`
	MaxBodyBytes      int64    `yaml:"max_body_bytes" json:"max_body_bytes"`
	// DefaultTenant 在请求未携带租户标识时使用；为空时取第一个租户。
	DefaultTenant string   `yaml:"default_tenant" json:"default_tenant"`
	IPAllow       []string `yaml:"ip_allow" json:"ip_allow"`
	IPDeny        []string `yaml:"ip_deny" json:"ip_deny"`
	CORS          *CORS    `yaml:"cors" json:"cors"`
	// Discovery 描述下游实例来源。
	Discovery *DiscoveryClientConfig `yaml:"discovery" json:"discovery"`
}

// ServerConfig 数据面配置
type ServerConfig struct {
	Host string
	Port int

	// ReadHeaderTimeout 只约束请求头，防止慢速 header 攻击。
	ReadHeaderTimeout time.Duration
	// IdleTimeout 约束 keep-alive 空闲连接。
	IdleTimeout time.Duration
	// ShutdownTimeout 是优雅停机的最长等待时间。
	ShutdownTimeout time.Duration

	MaxHeaderSize int
}

// AdminConfig 控制面配置
type AdminConfig struct {
	Mode string
	Host string
	Port int

	ReadTimeout  int
	WriteTimeout int
	IdleTimeout  int
	CloseTimeout int

	MaxHeaderSize int
	MaxBodySize   int64
}

// LoadBalancerConfig 负载均衡器配置
type LoadBalancerConfig struct {
	Strategy string
}

// DiscoveryClientConfig 注册中心客户端配置
type DiscoveryClientConfig struct {
	// Type 决定实例来源：static（配置写死）、nacos、consul。
	Type string `yaml:"type" json:"type"`
	// Address 是 "host:port" 的紧凑形式，Host/Port 分开写更显式。
	Host    string `yaml:"host" json:"host"`
	Port    int    `yaml:"port" json:"port"`
	Address string `yaml:"address" json:"address"`
	// Namespace 是注册中心命名空间。
	Namespace string `yaml:"namespace" json:"namespace"`
	// Protocol 是注册中心协议，nacos 支持 http/https；默认 http。
	Protocol string `yaml:"protocol" json:"protocol"`
	// RootPath 是注册中心的上下文路径（nacos 的 ContextPath，默认 /nacos）。
	RootPath string `yaml:"root_path" json:"root_path"`
	// LogLevel 是注册中心 SDK 的日志级别：debug|info|warn|error，默认 warn。
	LogLevel string `yaml:"log_level" json:"log_level"`
	// TimeoutMs 是注册中心单次查询超时，默认 5000 毫秒。
	TimeoutMs int `yaml:"timeout_ms" json:"timeout_ms"`
	// Instances 是 type=static 时的实例清单。
	Instances []StaticInstance `yaml:"instances" json:"instances"`
}

// ConfigClientConfig 配置中心客户端配置
type ConfigClientConfig struct {
	// Type 决定配置来源：file（本地文件）、nacos、apollo。
	Type string `yaml:"type" json:"type"`
	// Path 是 type=file 时的配置文档路径，同时作为默认 key。
	Path string `yaml:"path" json:"path"`
	// Format 是文档格式 yaml|json；为空时按 Path（或 key）的扩展名推断。
	Format string `yaml:"format" json:"format"`
	// Interval 是 type=file 的轮询间隔，默认 1s。
	// 用轮询而不是 fsnotify：容器里挂载的 ConfigMap 更新时 mtime 变化、
	// inode 替换等各种情形不一致，轮询比较内容反而最稳。
	Interval Duration `yaml:"interval" json:"interval"`
	// Address 是 "host:port" 的紧凑形式，Host/Port 分开写更显式。
	Host    string `yaml:"host" json:"host"`
	Port    int    `yaml:"port" json:"port"`
	Address string `yaml:"address" json:"address"`
	// Namespace 是注册/配置中心的命名空间（nacos 的 namespaceId）。
	Namespace string `yaml:"namespace" json:"namespace"`
	// Group 是配置分组，nacos 默认 DEFAULT_GROUP。
	Group string `yaml:"group" json:"group"`
	// DataId 是默认配置文档标识；Get 的 key 为空时用它。
	DataId string `yaml:"data_id" json:"data_id"`
	// Protocol 是配置中心协议，nacos 支持 http/https；默认 http。
	Protocol string `yaml:"protocol" json:"protocol"`
	// RootPath 是配置中心的上下文路径（nacos 的 ContextPath，默认 /nacos）。
	RootPath string `yaml:"root_path" json:"root_path"`
	// LogLevel 是 SDK 自己的日志级别：debug|info|warn|error，默认 warn。
	LogLevel string `yaml:"log_level" json:"log_level"`
	// TimeoutMs 是单次拉取超时，默认 5000 毫秒。
	TimeoutMs int `yaml:"timeout_ms" json:"timeout_ms"`
}

// Endpoint 统一 address 与 host/port 两种写法，与 DiscoveryClientConfig 保持一致。
func (c *ConfigClientConfig) Endpoint() (host string, port int, err error) {
	if c == nil {
		return "", 0, fmt.Errorf("config client: 配置为 nil")
	}
	if c.Address != "" {
		h, p, splitErr := net.SplitHostPort(c.Address)
		if splitErr != nil {
			return "", 0, fmt.Errorf("config client: address %q 需要 host:port 形式: %w", c.Address, splitErr)
		}
		n, convErr := strconv.Atoi(p)
		if convErr != nil || n <= 0 || n > 65535 {
			return "", 0, fmt.Errorf("config client: address %q 的端口非法", c.Address)
		}
		return h, n, nil
	}
	if c.Host == "" {
		return "", 0, fmt.Errorf("config client: 缺少 address（或 host/port）")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return "", 0, fmt.Errorf("config client: host %q 的 port %d 非法", c.Host, c.Port)
	}
	return c.Host, c.Port, nil
}

// Endpoint 解析注册中心地址，兼容 address 与 host/port 两种写法。
//
// 配置校验与客户端构造都调它，保证"校验通过的配置一定能构造出客户端"。
func (d *DiscoveryClientConfig) Endpoint() (host string, port int, err error) {
	if d == nil {
		return "", 0, fmt.Errorf("discovery: 配置为 nil")
	}
	if d.Address != "" {
		h, p, splitErr := net.SplitHostPort(d.Address)
		if splitErr != nil {
			return "", 0, fmt.Errorf("discovery: address %q 需要 host:port 形式: %w", d.Address, splitErr)
		}
		n, convErr := strconv.Atoi(p)
		if convErr != nil || n <= 0 || n > 65535 {
			return "", 0, fmt.Errorf("discovery: address %q 的端口非法", d.Address)
		}
		return h, n, nil
	}
	if d.Host == "" {
		return "", 0, fmt.Errorf("discovery: 缺少 address（或 host/port）")
	}
	if d.Port <= 0 || d.Port > 65535 {
		return "", 0, fmt.Errorf("discovery: host %q 的 port %d 非法", d.Host, d.Port)
	}
	return d.Host, d.Port, nil
}

// StaticInstance 是一条静态实例声明，需要自带租户/服务/集群定位——
// 实例不再挂在集群下面，因此三跳信息必须写在实例自己身上。
type StaticInstance struct {
	Tenant  string `yaml:"tenant" json:"tenant"`
	Service string `yaml:"service" json:"service"`
	Cluster string `yaml:"cluster" json:"cluster"`
	Addr    string `yaml:"addr" json:"addr"` // host:port
	Weight  int    `yaml:"weight" json:"weight"`
}

// CORS 是入口跨域策略。
type CORS struct {
	AllowOrigins     []string `yaml:"allow_origins" json:"allow_origins"`
	AllowMethods     []string `yaml:"allow_methods" json:"allow_methods"`
	AllowHeaders     []string `yaml:"allow_headers" json:"allow_headers"`
	ExposeHeaders    []string `yaml:"expose_headers" json:"expose_headers"`
	AllowCredentials bool     `yaml:"allow_credentials" json:"allow_credentials"`
	MaxAge           Duration `yaml:"max_age" json:"max_age"`
}

// Tenant 是路由与策略的隔离边界。
type Tenant struct {
	Name        string    `yaml:"name" json:"name"`
	Description string    `yaml:"description" json:"description"`
	Services    []Service `yaml:"services" json:"services"`
}

// Service 是逻辑服务，承载协议、超时与治理策略。
type Service struct {
	Name     string   `yaml:"name" json:"name"`
	Protocol string   `yaml:"protocol" json:"protocol"` // http | grpc | ai
	Timeout  Duration `yaml:"timeout" json:"timeout"`

	Routes   []Route   `yaml:"routes" json:"routes"`
	Clusters []Cluster `yaml:"clusters" json:"clusters"`

	// 以下策略可被 Route 级别的同名字段整体覆盖。
	Auth      *AuthPolicy      `yaml:"auth" json:"auth"`
	Validate  *ValidatePolicy  `yaml:"validate" json:"validate"`
	RateLimit *RateLimitPolicy `yaml:"rate_limit" json:"rate_limit"`
	Breaker   *BreakerPolicy   `yaml:"breaker" json:"breaker"`
	Retry     *RetryPolicy     `yaml:"retry" json:"retry"`
	Headers   *HeaderPolicy    `yaml:"headers" json:"headers"`
}

// Route 是租户空间内的一条匹配规则。
type Route struct {
	ID      string            `yaml:"id" json:"id"`
	Path    string            `yaml:"path" json:"path"` // /v1/orders 或 /v1/orders/*（前缀匹配）；只写 Rule 时可省略
	Methods []string          `yaml:"methods" json:"methods"`
	Host    string            `yaml:"host" json:"host"`
	Headers map[string]string `yaml:"headers" json:"headers"`
	Query   map[string]string `yaml:"query" json:"query"`

	// Rule 是路由匹配的 DSL 表达式，启动时编译一次，请求期只做求值。
	// 例如：http.method == "POST" && matches(http.path, "^/v1/orders/[0-9]+$")
	//
	// 与 Path/Methods/Host/Headers/Query 是"与"关系：静态字段先做快速筛选，
	// 通过之后再求值 Rule。需要表达这些字段写不出来的条件（IP 网段、
	// 多值枚举、变量提取结果）时才用 Rule。
	Rule string `yaml:"rule" json:"rule"`

	// Priority 是同一租户内多条路由同时命中时的优先级，大的先匹配。
	// 0 表示按结构排序（精确路径 > 前缀路径 > 仅 Rule）。
	Priority int `yaml:"priority" json:"priority"`

	// Cluster 是默认目标集群
	Cluster string   `yaml:"cluster" json:"cluster"`
	Source  string   `yaml:"source" json:"source"`
	Timeout Duration `yaml:"timeout" json:"timeout"`

	Auth      *AuthPolicy      `yaml:"auth" json:"auth"`
	Validate  *ValidatePolicy  `yaml:"validate" json:"validate"`
	RateLimit *RateLimitPolicy `yaml:"rate_limit" json:"rate_limit"`
	Breaker   *BreakerPolicy   `yaml:"breaker" json:"breaker"`
	Retry     *RetryPolicy     `yaml:"retry" json:"retry"`
}

// Cluster 是部署单元，负责地域、版本与实例池。
type Cluster struct {
	Name     string            `yaml:"name" json:"name"`
	Region   string            `yaml:"region" json:"region"`
	Version  string            `yaml:"version" json:"version"`
	LB       string            `yaml:"lb" json:"lb"` // round_robin | random | least_conn | consistent_hash
	Metadata map[string]string `yaml:"metadata" json:"metadata"`

	HealthCheck *HealthCheckPolicy `yaml:"health_check" json:"health_check"`
}

// Instance 是最终的执行节点。
type Instance struct {
	ID       string            `yaml:"id" json:"id"`
	Addr     string            `yaml:"addr" json:"addr"` // host:port
	Weight   int               `yaml:"weight" json:"weight"`
	Metadata map[string]string `yaml:"metadata" json:"metadata"`
}

// HealthCheckPolicy 描述主动健康检查参数。
type HealthCheckPolicy struct {
	Path             string   `yaml:"path" json:"path"`
	Interval         Duration `yaml:"interval" json:"interval"`
	Timeout          Duration `yaml:"timeout" json:"timeout"`
	FailureThreshold int      `yaml:"failure_threshold" json:"failure_threshold"`
	SuccessThreshold int      `yaml:"success_threshold" json:"success_threshold"`
}

// AuthPolicy 描述认证方式。
type AuthPolicy struct {
	Type        string   `yaml:"type" json:"type"` // none | api_key | jwt
	Header      string   `yaml:"header" json:"header"`
	Keys        []string `yaml:"keys" json:"keys"`     // api_key
	Secret      string   `yaml:"secret" json:"secret"` // jwt HS256
	Issuer      string   `yaml:"issuer" json:"issuer"`
	Audience    string   `yaml:"audience" json:"audience"`
	TenantClaim string   `yaml:"tenant_claim" json:"tenant_claim"`
	Scopes      []string `yaml:"scopes" json:"scopes"`
}

// ValidatePolicy 描述请求参数校验。
type ValidatePolicy struct {
	MaxBodyBytes       int64    `yaml:"max_body_bytes" json:"max_body_bytes"`
	ContentTypes       []string `yaml:"content_types" json:"content_types"`
	RequiredHeaders    []string `yaml:"required_headers" json:"required_headers"`
	RequiredQuery      []string `yaml:"required_query" json:"required_query"`
	RequiredBodyFields []string `yaml:"required_body_fields" json:"required_body_fields"`
}

// RateLimitPolicy 描述限流。
type RateLimitPolicy struct {
	RPS       float64  `yaml:"rps" json:"rps"`
	Burst     int      `yaml:"burst" json:"burst"`
	Key       string   `yaml:"key" json:"key"`             // ip | tenant | route | user
	Algorithm string   `yaml:"algorithm" json:"algorithm"` // token_bucket | sliding_window
	Window    Duration `yaml:"window" json:"window"`
}

// BreakerPolicy 描述熔断。
type BreakerPolicy struct {
	ErrorRate        float64  `yaml:"error_rate" json:"error_rate"` // 0-1
	MinRequests      int      `yaml:"min_requests" json:"min_requests"`
	OpenTimeout      Duration `yaml:"open_timeout" json:"open_timeout"`
	HalfOpenRequests int      `yaml:"half_open_requests" json:"half_open_requests"`
}

// RetryPolicy 描述重试。
type RetryPolicy struct {
	MaxAttempts int      `yaml:"max_attempts" json:"max_attempts"`
	Backoff     Duration `yaml:"backoff" json:"backoff"`
	MaxBackoff  Duration `yaml:"max_backoff" json:"max_backoff"`
	Jitter      float64  `yaml:"jitter" json:"jitter"`     // 0-1
	RetryOn     []int    `yaml:"retry_on" json:"retry_on"` // 额外视为可重试的下游状态码
}

// HeaderPolicy 描述请求/响应头改写。
type HeaderPolicy struct {
	RequestAdd     map[string]string `yaml:"request_add" json:"request_add"`
	RequestRemove  []string          `yaml:"request_remove" json:"request_remove"`
	ResponseAdd    map[string]string `yaml:"response_add" json:"response_add"`
	ResponseRemove []string          `yaml:"response_remove" json:"response_remove"`
}

// ==========================================
// 1. 核心网关配置结构体 (Core Configuration)
// ==========================================

// ApiConfig 代表单个 API 的完整生命周期与治理定义
type ApiConfig struct {
	ID       string            `json:"id" yaml:"id"`             // 唯一标识（通常为 UUID 或雪花ID）
	Name     string            `json:"name" yaml:"name"`         // API 易读名称
	Group    string            `json:"group" yaml:"group"`       // 业务分组/Namespace
	Version  int               `json:"version" yaml:"version"`   // 版本号
	Labels   map[string]string `json:"labels" yaml:"labels"`     // 标签元数据（用于灰度发布、租户隔离、环境区分）
	Status   string            `json:"status" yaml:"status"`     // 状态：DRAFT(草稿), ONLINE(上线), OFFLINE(下线)
	Priority int               `json:"priority" yaml:"priority"` // 路由匹配优先级，当多个 DSL 同时满足时，高优先级优先命中
	Request  InboundRequest    `json:"request" yaml:"request"`   // 入站客户端请求定义（基于 DSL）
	Plugins  []PluginConfig    `json:"plugins" yaml:"plugins"`   // 插件链（责任链模式，如限流、鉴权等）
	Pipeline PipelineConfig    `json:"pipeline" yaml:"pipeline"` // 后端服务调用与编排拓扑
	Response OutboundResponse  `json:"response" yaml:"response"` // 出站响应聚合、清洗与异常转换
	Filters  any
	Invokers any
}

// ==========================================
// 2. 入站请求定义 (Inbound Request)
// ==========================================

// InboundRequest 将匹配规则彻底抽象。不再硬编码 Host/Path，全量交由 DSL 表达式决策。
type InboundRequest struct {
	// Expression 存放您的 DSL 匹配语句，例如：
	Expression string `json:"expression" yaml:"expression"`

	// VarsMapping 定义了从当前请求中通过 DSL 提取并保存的“命名捕获变量”，用于后续的 Plugins 或 Invokers。
	VarsMapping []VarsExtractRule `json:"vars_mapping" yaml:"vars_mapping"`
}

// VarsExtractRule 定义网关运行时上下文的变量提取规则
type VarsExtractRule struct {
	VarName   string `json:"var_name" yaml:"var_name"`     // 提取后的变量名，如 "order_id"
	DSLSource string `json:"dsl_source" yaml:"dsl_source"` // 提取源的 DSL 表达式，如 "http.path.regex_capture('/v1/orders/([0-9]+)', 1)"
}

// ==========================================
// 3. 声明式插件定义 (Dynamic Plugins)
// ==========================================

// PluginConfig 采用工业级标准，通过延迟解析提升网关高并发下的核心性能
type PluginConfig struct {
	Name    string `json:"name" yaml:"name"`       // 插件名称
	Type    string `json:"type" yaml:"type"`       // 插件阶段：Auth, Traffic, Security, Observability
	Enabled bool   `json:"enabled" yaml:"enabled"` // 是否启用
}

// ==========================================
// 4. 后端服务编排流水线 (Pipeline & Invokers)
// ==========================================

// PipelineConfig 定义了网关后端多个 Invoker 的调用策略
type PipelineConfig struct {
	Policy        string        `json:"policy" yaml:"policy"`                   // 编排模式: SINGLE(单服务), CONCURRENT(并发调用), DAG(有向无环图串行依赖)
	Aggregation   bool          `json:"aggregation" yaml:"aggregation"`         // 是否合并多个后端 Node 的结果并统一输出
	StopOnFailure bool          `json:"stop_on_failure" yaml:"stop_on_failure"` // 当并行或串行调用时，单个节点失败是否立即截断并返回
	Nodes         []BackendNode `json:"nodes" yaml:"nodes"`                     // 具体的后端服务调用节点列表
}

// BackendNode 代表具体的一个后端服务节点（RPC/HTTP/函数）
type BackendNode struct {
	ID             string        `json:"id" yaml:"id"`                           // 节点唯一 ID，用于被其他节点作为依赖引用，或在结果集中标识
	Protocol       string        `json:"protocol" yaml:"protocol"`               // 协议类型: HTTP, gRPC, Dubbo, Thrift, NATS, FUNCTION
	UpstreamType   string        `json:"upstream_type" yaml:"upstream_type"`     // 寻址/发现方式: DISCOVERY(注册中心), STATIC(静态IP/集群)
	UpstreamName   string        `json:"upstream_name" yaml:"upstream_name"`     // 注册中心的服务名(如 "user-service") 或静态域名/IP集群标识
	Method         string        `json:"method" yaml:"method"`                   // 后端接口地址（HTTP 的 Path，或者是 gRPC 的 FullMethodName）
	Timeout        time.Duration `json:"timeout" yaml:"timeout"`                 // 该节点的单次调用超时控制
	RetryConfig    *RetryConfig  `json:"retry_config" yaml:"retry_config"`       // 独立重试策略，若为 null 则使用网关全局默认值
	CircuitBreaker *CBConfig     `json:"circuit_breaker" yaml:"circuit_breaker"` // 独立熔断降级策略

	// Condition 节点准入 DSL：支持动态灰度或条件编排控制流。
	Condition string `json:"condition" yaml:"condition"`

	DependsOn    []string      `json:"depends_on" yaml:"depends_on"`       // 依赖的节点 ID 集合（仅在 DAG 模式下生效，指示编排执行顺序）
	ParamMapping []MappingRule `json:"param_mapping" yaml:"param_mapping"` // 后端入参组装与映射规则
}

// MappingRule 将网关收集到的入参动态投递给特定的后端字段
type MappingRule struct {
	Target string `json:"target" yaml:"target"` // 后端接收的字段路径 (例如 "headers.X-User-Id" 或 "body.order_id")
	Source string `json:"source" yaml:"source"` // 数据源表达式，支持读取请求体或上下文变量，如 "\$.ctx.vars.order_id"
}

// RetryConfig 重试策略
type RetryConfig struct {
	Attempts int           `json:"attempts" yaml:"attempts"` // 最大重试次数
	Backoff  time.Duration `json:"backoff" yaml:"backoff"`   // 重试间隔避退时间
}

// CBConfig 熔断器策略
type CBConfig struct {
	FailureRatio  string        `json:"failure_ratio" yaml:"failure_ratio"`   // 触发熔断的失败率阈值 (如 "50%")
	TimeoutWindow time.Duration `json:"timeout_window" yaml:"timeout_window"` // 熔断后尝试半开（恢复）的时间窗口
}

// ==========================================
// 5. 出站响应控制 (Outbound Response)
// ==========================================

// OutboundResponse 负责网关对外的统一格式化、结果裁剪、静态信息植入和异常治理
type OutboundResponse struct {
	ContentType     string            `json:"content_type" yaml:"content_type"`         // 返回给前端的 Content-Type，默认 "application/json"
	ConstantHeaders map[string]string `json:"constant_headers" yaml:"constant_headers"` // 强制追加的静态响应头（如网关签名、TraceID、防护头）
	Cookies         []CookieSetting   `json:"cookies" yaml:"cookies"`                   // 动态/静态 Cookie 植入规则
	BodyTransform   *TransformRule    `json:"body_transform" yaml:"body_transform"`     // 声明式响应结构体再组装与渲染
	ErrorMapping    []ErrorMapRule    `json:"error_mapping" yaml:"error_mapping"`       // 全局网关异常、熔断、超时的拦截与“翻译”
}

// CookieSetting Cookie 的自动化注入规范
type CookieSetting struct {
	Name     string `json:"name" yaml:"name"`
	Value    string `json:"value" yaml:"value"` // 支持动态表达式获取，例如 "\$.nodes.node_auth.body.token"
	Path     string `json:"path" yaml:"path"`
	Domain   string `json:"domain" yaml:"domain"`
	MaxAge   int    `json:"max_age" yaml:"max_age"`
	HTTPOnly bool   `json:"http_only" yaml:"http_only"`
	Secure   bool   `json:"secure" yaml:"secure"`
}

// TransformRule 响应体转换规则
type TransformRule struct {
	Type     string `json:"type" yaml:"type"`         // 转换技术：MAPPING(简单的表达式字段映射), GO_TEMPLATE(标准Go模板渲染), NONE(原样返回或默认合并)
	Template string `json:"template" yaml:"template"` // 当为 GO_TEMPLATE 时存放模板字符串，当为 MAPPING 时存放自定义 JSON 映射配置
}

// ErrorMapRule 错误屏蔽与转换规则
type ErrorMapRule struct {
	// MatchCondition 触发错误拦截的 DSL 表达式，例如 "\$.nodes.node_auth.status_code == 401"
	MatchCondition string `json:"match_condition" yaml:"match_condition"`
	HTTPStatus     int    `json:"http_status" yaml:"http_status"`     // 暴露给前端的标准 HTTP 状态码
	ResponseBody   string `json:"response_body" yaml:"response_body"` // 隐藏敏感后端堆栈后的标准化 JSON 错误提示
}
