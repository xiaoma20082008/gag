package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// DefaultErrorResponse 生成标准错误响应体。
//
// 响应结构固定为 code/message/request_id/trace_id，便于客户端与网关日志对齐。
// 由 Chain 的 finalize 调用，也可被模块主动用于把结构化错误转成响应。
func DefaultErrorResponse(c *RequestContext, err *Error) {
	if err == nil {
		err = NewError(CodeInternal, "unknown error")
	}
	body, _ := json.Marshal(map[string]string{
		"code":       string(err.Code),
		"message":    err.Message,
		"request_id": c.RequestID,
		"trace_id":   c.TraceID,
	})
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	if err.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(int(err.RetryAfter.Seconds()+0.5)))
	}
	c.SetResponse(err.Status, h, body)
}
