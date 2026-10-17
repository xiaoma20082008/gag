package modules

import (
	"net/http"
	"strconv"
	"time"

	"gag/internal/gateway"
	"gag/internal/observability/logging"
	"gag/internal/observability/metrics"
)

// ResponseModule 在拿到下游响应后做最后一道规范化。
//
// 与 CORS 同处 read_response 回调点，但 order 更小：先把响应收拾干净，
// 再让 CORS 之类的模块往上加头。
type ResponseModule struct{ st State }

// NewResponse 创建响应规范化模块。
func NewResponse(st State) gateway.Module { return &ResponseModule{st: st} }

func (m *ResponseModule) Name() string { return "mod_response" }

func (m *ResponseModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookReadResponse, 10, "mod_response", "response.normalize", m.handle)
	return nil
}

func (m *ResponseModule) handle(c *gateway.RequestContext) gateway.Action {
	if c.Response == nil {
		// finalize 通常已经保证这里非空；这是给自定义装配留的兜底。
		if err := c.Error(); err != nil {
			gateway.DefaultErrorResponse(c, err)
		} else {
			c.SetResponse(http.StatusOK, http.Header{}, nil)
		}
	}

	// 下游返回的状态码与响应体原样透传：网关替下游改写业务状态码
	// 会让客户端无法判断请求到底成功没有。这里只清理传输层遗留的头。
	stripHopByHop(c.Response.Header)

	set := setOf(c, m.st)
	if set == nil || set.Headers == nil {
		return gateway.ActionGoOn
	}
	if c.Response.Header == nil {
		c.Response.Header = http.Header{}
	}
	for k, v := range set.Headers.ResponseAdd {
		c.Response.Header.Set(k, v)
	}
	for _, k := range set.Headers.ResponseRemove {
		c.Response.Header.Del(k)
	}
	return gateway.ActionGoOn
}

// ---------- 访问日志 ----------

// AccessLogModule 在 request_finish 输出访问日志。
type AccessLogModule struct {
	logger *logging.Logger
}

// NewAccessLog 创建访问日志模块；logger 为 nil 时返回 nil（不挂载）。
func NewAccessLog(logger *logging.Logger) gateway.Module {
	if logger == nil {
		return nil
	}
	return &AccessLogModule{logger: logger}
}

func (m *AccessLogModule) Name() string { return "mod_access_log" }

func (m *AccessLogModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookRequestFinish, 20, "mod_access_log", "access.log", m.handle)
	return nil
}

func (m *AccessLogModule) handle(c *gateway.RequestContext) gateway.Action {
	path := ""
	if c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	entry := logging.AccessEntry{
		TraceID:    c.TraceID,
		RequestID:  c.RequestID,
		ClientIP:   c.ClientIP,
		Tenant:     c.Tenant,
		Route:      routeLabel(c),
		Service:    c.Service,
		Cluster:    c.Cluster,
		Instance:   c.Instance,
		Method:     c.Request.Method,
		Path:       path,
		Status:     statusOf(c),
		LatencyMS:  millis(c.Latency()),
		UpstreamMS: millis(c.UpstreamLatency),
		Attempts:   c.Attempts,
		AuthResult: c.AuthResult,
		RateLimit:  c.RateLimitResult,
		Circuit:    c.CircuitState,
		UserAgent:  c.UserAgent,
	}
	if err := c.Error(); err != nil {
		entry.ErrorCode = string(err.Code)
	}
	m.logger.Access(entry)
	return gateway.ActionGoOn
}

// ---------- 指标 ----------

// MetricsModule 在 request_finish 上报请求数、耗时与治理计数。
type MetricsModule struct {
	metrics *metrics.Gateway
}

// NewMetrics 创建指标模块；metrics 为 nil 时返回 nil（不挂载）。
func NewMetrics(gw *metrics.Gateway) gateway.Module {
	if gw == nil {
		return nil
	}
	return &MetricsModule{metrics: gw}
}

func (m *MetricsModule) Name() string { return "mod_metrics" }

func (m *MetricsModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookRequestFinish, 30, "mod_metrics", "metrics.record", m.handle)
	return nil
}

func (m *MetricsModule) handle(c *gateway.RequestContext) gateway.Action {
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
	return gateway.ActionGoOn
}

const rateLimitRejected = "rejected"

// ---------- trace 收尾 ----------

// TraceModule 在 request_finish 结束 span。
type TraceModule struct{}

// NewTrace 创建 trace 收尾模块。
func NewTrace() gateway.Module { return &TraceModule{} }

func (m *TraceModule) Name() string { return "mod_trace" }

func (m *TraceModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookRequestFinish, 40, "mod_trace", "trace.end", m.handle)
	return nil
}

func (m *TraceModule) handle(c *gateway.RequestContext) gateway.Action {
	span := spanOf(c)
	if span == nil {
		// 自定义装配没有挂 mod_request_id 时没有 span 可收尾。
		return gateway.ActionGoOn
	}
	span.SetAttr("route", routeLabel(c))
	span.SetAttr("service", c.Service)
	span.SetAttr("cluster", c.Cluster)
	span.SetAttr("instance", c.Instance)
	span.SetAttr("status", strconv.Itoa(statusOf(c)))
	span.SetAttr("attempts", strconv.Itoa(c.Attempts))
	span.SetAttr("auth", c.AuthResult)
	if c.UpstreamLatency > 0 {
		span.SetAttr("upstream_ms", formatMillis(c.UpstreamLatency))
	}
	if err := c.Error(); err != nil {
		span.SetAttr("error_code", string(err.Code))
	}
	span.End()
	return gateway.ActionGoOn
}

// millis 把耗时转成毫秒浮点数，与日志字段单位保持一致。
func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

func formatMillis(d time.Duration) string {
	return strconv.FormatFloat(millis(d), 'f', 3, 64)
}
