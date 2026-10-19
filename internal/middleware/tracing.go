package middleware

import (
	"strconv"
	"time"

	"gag/internal/core"
)

// Trace 在 request_finish 结束 span。
//
// span 由 request_id 中间件创建；这里只负责补齐路由、实例、状态码等
// 只有走到收尾阶段才确定的属性，然后结束它。
type Trace struct{}

// NewTrace 创建 trace 收尾中间件。
func NewTrace() core.Module { return &Trace{} }

func (m *Trace) Name() string { return "mw_trace" }

func (m *Trace) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookRequestFinish, 40, "mw_trace", "trace.end", m.handle)
	return nil
}

func (m *Trace) handle(c *core.RequestContext) core.Action {
	span := spanOf(c)
	if span == nil {
		// 自定义装配没有挂 request_id 时没有 span 可收尾。
		return core.ActionGoOn
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
	return core.ActionGoOn
}

func formatMillis(d time.Duration) string {
	return strconv.FormatFloat(millis(d), 'f', 3, 64)
}
