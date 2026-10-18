package metrics

import "github.com/prometheus/client_golang/prometheus"

// Gateway 汇总网关标准指标。
//
// 指标命名遵循 Prometheus 惯例：<namespace>_<name>_<unit>，
// 计数器以 _total 结尾，耗时统一用秒。标签顺序是契约的一部分，
// 调用方必须按声明顺序传值，否则会得到一条标签错位的指标。
type Gateway struct {
	// Requests 是入口请求总数，按路由/方法/状态码/租户维度切分。
	Requests *prometheus.CounterVec
	// Duration 是网关整体处理耗时。
	Duration *prometheus.HistogramVec
	// Upstream 是下游调用耗时。
	Upstream *prometheus.HistogramVec
	// InFlight 是各阶段并发中的请求数。
	InFlight *prometheus.GaugeVec
	// RateLimited 记录限流命中。
	RateLimited *prometheus.CounterVec
	// CircuitEvents 记录熔断状态迁移。
	CircuitEvents *prometheus.CounterVec
	// ConfigApply 记录配置下发结果。
	ConfigApply *prometheus.CounterVec
}

// NewGateway 注册并返回网关标准指标集。
//
// 重复注册同名指标会让 prometheus panic，因此同一个 Registry 只应调用一次；
// 需要多份时（如测试）请各自创建独立的 Registry。
func NewGateway(r *Registry) *Gateway {
	g := &Gateway{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "requests_total",
			Help:      "Total number of requests handled by the gateway.",
		}, []string{"route", "method", "status", "tenant"}),

		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "request_duration_seconds",
			Help:      "End-to-end request latency observed at the gateway.",
			Buckets:   DefaultBuckets,
		}, []string{"route", "status"}),

		Upstream: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "upstream_duration_seconds",
			Help:      "Downstream invocation latency.",
			Buckets:   DefaultBuckets,
		}, []string{"service", "cluster"}),

		InFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "requests_in_flight",
			Help:      "Number of requests currently being processed.",
		}, []string{"phase"}),

		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "rate_limited_total",
			Help:      "Number of requests rejected by rate limiting.",
		}, []string{"route", "key"}),

		CircuitEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "circuit_events_total",
			Help:      "Circuit breaker state transitions.",
		}, []string{"route", "event"}),

		ConfigApply: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "config_apply_total",
			Help:      "Configuration apply attempts by result.",
		}, []string{"result"}),
	}

	r.MustRegister(
		g.Requests,
		g.Duration,
		g.Upstream,
		g.InFlight,
		g.RateLimited,
		g.CircuitEvents,
		g.ConfigApply,
	)
	return g
}
