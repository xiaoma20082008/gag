package middleware

import (
	"time"

	"gag/internal/core"
	"gag/internal/telemetry/logging"
)

// AccessLog 在 request_finish 输出访问日志。
//
// 挂在收尾阶段而不是响应写出之前：此时状态码、重试次数、错误码都已确定，
// 一条日志就能回答"这个请求发生了什么"，不需要再去关联多条。
type AccessLog struct {
	logger *logging.Logger
}

// NewAccessLog 创建访问日志中间件；logger 为 nil 时返回 nil（不挂载）。
func NewAccessLog(logger *logging.Logger) core.Module {
	if logger == nil {
		return nil
	}
	return &AccessLog{logger: logger}
}

func (m *AccessLog) Name() string { return "mw_access_log" }

func (m *AccessLog) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookRequestFinish, 20, "mw_access_log", "access.log", m.handle)
	return nil
}

func (m *AccessLog) handle(c *core.RequestContext) core.Action {
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
	return core.ActionGoOn
}

// millis 把耗时转成毫秒浮点数，与日志字段单位保持一致。
func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
