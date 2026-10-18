package middleware

import (
	"fmt"

	"gag/internal/core"
	gerr "gag/pkg/errors"
)

// Recover 把 panic 折算成链路错误。
//
// 由 router.Chain 在每个回调点外层 defer 调用：一条请求的异常不能让
// 整个连接/进程挂掉，也不能让连接直接断掉——请求应当带着一个
// 合法的 500 响应结束，访问日志与指标才留得下现场。
func Recover(c *core.RequestContext, point string) {
	rec := recover()
	if rec == nil {
		return
	}
	c.Fail(gerr.New(gerr.CodeInternal, "请求处理发生 panic").
		WithCause(fmt.Errorf("%s: %v", point, rec)))
}
