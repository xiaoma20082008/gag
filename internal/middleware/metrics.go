package middleware

import (
	"strconv"

	"gag/internal/core"
	"gag/internal/telemetry/metrics"
)

const rateLimitRejected = "rejected"

// Metrics 在 request_finish 上报请求数、耗时与治理计数。
type Metrics struct {
	metrics *metrics.Gateway
}

// NewMetrics 创建指标中间件；metrics 为 nil 时返回 nil（不挂载）。
func NewMetrics(gw *metrics.Gateway) core.Module {
	if gw == nil {
		return nil
	}
	return &Metrics{metrics: gw}
}

func (m *Metrics) Name() string { return "mw_metrics" }

func (m *Metrics) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookRequestFinish, 30, "mw_metrics", "metrics.record", m.handle)
	return nil
}

func (m *Metrics) handle(c *core.RequestContext) core.Action {
	route := routeLabel(c)
	status := strconv.Itoa(statusOf(c))

	m.metrics.Requests.WithLabelValues(route, c.Request.Method, status, c.Tenant).Inc()
	m.metrics.Duration.WithLabelValues(route, status).Observe(c.Latency().Seconds())
	if c.UpstreamLatency > 0 {
		m.metrics.Upstream.WithLabelValues(c.Service, c.Cluster).Observe(c.UpstreamLatency.Seconds())
	}
	if c.RateLimitResult == rateLimitRejected {
		m.metrics.RateLimited.WithLabelValues(route, rateLimitKeyOf(c)).Inc()
	}
	// 熔断器不向外发事件，这里记录"请求观测到的降级状态"：
	// 只有非 closed 才计数，否则这个指标会被正常流量刷满而失去告警价值。
	if c.CircuitState != "" && c.CircuitState != "closed" {
		m.metrics.CircuitEvents.WithLabelValues(route, c.CircuitState).Inc()
	}
	return core.ActionGoOn
}
