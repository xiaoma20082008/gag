package middleware

import (
	"net/http"

	"gag/internal/core"
)

// BodyLimit 在入口限制请求体大小。
//
// 用 http.MaxBytesReader 而不是读完整体再判断：超大请求在读的过程中就该被掐断，
// 否则网关要先替它付一遍内存与带宽。
type BodyLimit struct{ limit int64 }

// NewBodyLimit 创建请求体限制中间件；limit <= 0 表示不限制（不挂载）。
func NewBodyLimit(limit int64) core.Module {
	if limit <= 0 {
		return nil
	}
	return &BodyLimit{limit: limit}
}

func (m *BodyLimit) Name() string { return "mw_body_limit" }

func (m *BodyLimit) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookBeforeRoute, 20, "mw_body_limit", "body.limit", m.handle)
	return nil
}

func (m *BodyLimit) handle(c *core.RequestContext) core.Action {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return core.ActionGoOn
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, m.limit)
	return core.ActionGoOn
}
