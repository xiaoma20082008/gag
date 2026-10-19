package router

import (
	"net/http"
	"time"

	"gag/internal/core"
	"gag/internal/middleware"
)

// Hooks 是框架内建的四步：租户查找、路由查找、下游调用与重试决策。
//
// 它们与回调点的区别是：回调点可以被插件无限扩展，而这里几步决定了
// "后续模块能看到什么上下文"，因此必须内建，不能做成模块。
// （对应 BFE 的 findProduct / findCluster / clusterInvoke。）
type Hooks struct {
	// ResolveTenant 确定请求租户。
	ResolveTenant func(*core.RequestContext) error
	// ResolveRoute 完成路由查找并填充 c.RouteID/Service 等字段。
	ResolveRoute func(*core.RequestContext) error
	// Forward 执行一次下游尝试；成功时负责把下游响应写入 c.Response。
	Forward func(*core.RequestContext) error
	// MaxAttempts 返回最大尝试次数；为 nil 或 <=1 时不重试。
	MaxAttempts func(*core.RequestContext) int
	// Retryable 判断本次失败是否值得再试（结合幂等性与状态码）。
	// err 是本次尝试的错误，只有放弃重试时才会被记进 c.Error()。
	Retryable func(c *core.RequestContext, err error, attempt int) bool
	// Backoff 返回第 attempt 次失败后、第 attempt+1 次尝试前的等待时间。
	// 为 nil 或 <=0 表示立即重试。等待可被客户端断连取消。
	Backoff func(c *core.RequestContext, attempt int) time.Duration
}

// Chain 是装配完成的执行链。
//
// 没有"阶段"概念，只有回调点；每次尝试都会触发 HookForward，
// 因此重试对其他模块可见。
type Chain struct {
	points [core.HookPointCount][]core.NamedHandler
	hooks  Hooks
}

// NewChain 用回调表与内建钩子装配执行链。
func NewChain(cbs *core.Callbacks, hooks Hooks) *Chain {
	if cbs == nil {
		cbs = core.NewCallbacks()
	}
	cbs.Sort()
	ch := &Chain{}
	for point := core.HookBeforeRoute; point <= core.HookRequestFinish; point++ {
		ch.points[point] = cbs.Points[point]
	}
	if hooks.ResolveTenant == nil || hooks.ResolveRoute == nil {
		panic("router: ResolveTenant/ResolveRoute 是必填钩子")
	}
	if hooks.Forward == nil {
		panic("router: Forward 是必填钩子")
	}
	ch.hooks = hooks
	return ch
}

// Topology 按回调点展开执行链，用于启动日志与排障。
func (ch *Chain) Topology() map[string][]string {
	cbs := &core.Callbacks{}
	for point := core.HookBeforeRoute; point <= core.HookRequestFinish; point++ {
		cbs.Points[point] = ch.points[point]
	}
	return cbs.Topology()
}

// Fire 执行某个回调点，返回第一个非 GoOn 的结果。
func (ch *Chain) Fire(point core.HookPoint, c *core.RequestContext) core.Action {
	for _, h := range ch.points[point] {
		if action := h.Handler(c); action != core.ActionGoOn {
			return action
		}
	}
	return core.ActionGoOn
}

// ServeHTTP 实现 http.Handler：跑完整条链并把响应写回客户端。
func (ch *Chain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := core.NewRequestContext(w, r)
	ch.execute(c)
	ch.finish(c)

	if c.CloseConn {
		// 不写任何字节，连接随 handler 返回而关闭。
		return
	}
	finalize(c)
	c.Response.WriteTo(w)
}

// execute 推进请求主链：before_route → 租户 → found_route → 路由 → after_route → 转发 →
// read_response。
//
// 早退（Respond/Finish）与 panic 都走同一条收尾路径：先由 finalize 保证 c.Response
// 就绪，再触发 read_response。否则 CORS 合并、hop-by-hop 清理这类响应改写
// 只对成功路径生效，被治理拒绝的请求会漏掉这些头。
func (ch *Chain) execute(c *core.RequestContext) {
	action := ch.runStages(c)
	if action == core.ActionClose {
		c.CloseConn = true
		return
	}
	finalize(c)
	ch.fireSafely(core.HookReadResponse, c)
}

// runStages 执行请求阶段并兜住 panic：一条请求的异常不能让整个连接/进程挂掉。
func (ch *Chain) runStages(c *core.RequestContext) (action core.Action) {
	defer middleware.Recover(c, "request")

	if action = ch.Fire(core.HookBeforeRoute, c); action != core.ActionGoOn {
		return action
	}
	if err := ch.hooks.ResolveTenant(c); err != nil {
		c.Fail(err)
		return core.ActionFinish
	}
	if action = ch.Fire(core.HookFoundRoute, c); action != core.ActionGoOn {
		return action
	}
	if err := ch.hooks.ResolveRoute(c); err != nil {
		c.Fail(err)
		return core.ActionFinish
	}
	if action = ch.Fire(core.HookAfterRoute, c); action != core.ActionGoOn {
		return action
	}
	return ch.forward(c)
}

// forward 执行下游调用，每一次尝试都先触发 HookForward。
//
// 这样熔断、重试之外的模块也能看到"这是第几次尝试"，
// 而不是把整个循环藏在一个模块内部。
func (ch *Chain) forward(c *core.RequestContext) core.Action {
	max := 1
	if ch.hooks.MaxAttempts != nil {
		max = ch.hooks.MaxAttempts(c)
	}
	if max < 1 {
		max = 1
	}
	for attempt := 1; ; attempt++ {
		c.Attempts = attempt

		if action := ch.Fire(core.HookForward, c); action != core.ActionGoOn {
			return action
		}
		err := ch.hooks.Forward(c)
		if err == nil {
			return core.ActionGoOn
		}
		if attempt >= max || !ch.retryable(c, err, attempt) {
			// 只在最终失败时记录错误：中间次失败是"还在重试"，
			// 不是链路的错误；否则重试成功后 c.Error() 仍带着旧错误。
			if !c.Failed() {
				c.Fail(err)
			}
			return core.ActionGoOn
		}
		if !ch.waitBeforeRetry(c, attempt) {
			// 客户端已断开或 ctx 取消，没必要继续消耗下游配额。
			if !c.Failed() {
				c.Fail(err)
			}
			return core.ActionGoOn
		}
	}
}

func (ch *Chain) retryable(c *core.RequestContext, err error, attempt int) bool {
	return ch.hooks.Retryable != nil && ch.hooks.Retryable(c, err, attempt)
}

// waitBeforeRetry 等待退避时间；返回 false 表示等待被取消。
func (ch *Chain) waitBeforeRetry(c *core.RequestContext, attempt int) bool {
	if ch.hooks.Backoff == nil {
		return true
	}
	d := ch.hooks.Backoff(c, attempt)
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.Context().Done():
		return false
	case <-timer.C:
		return true
	}
}

// finish 无条件执行收尾回调：日志、指标、trace 不能因为请求失败而丢。
//
// 返回值一律忽略，且每个模块单独兜 panic，
// 一个收尾模块出错不会连带其余模块一起不执行。
func (ch *Chain) finish(c *core.RequestContext) {
	for _, h := range ch.points[core.HookRequestFinish] {
		ch.runFinisher(h, c)
	}
}

func (ch *Chain) runFinisher(h core.NamedHandler, c *core.RequestContext) {
	defer func() {
		// 收尾阶段的异常不应影响已生成的响应。
		_ = recover()
	}()
	h.Handler(c)
}

// fireSafely 执行回调点并兜住 panic：异常折算成 internal_error，
// 请求仍然带着一个合法响应结束，而不是把连接直接断掉。
func (ch *Chain) fireSafely(point core.HookPoint, c *core.RequestContext) {
	defer middleware.Recover(c, point.String())
	ch.Fire(point, c)
}

// finalize 保证链路至少产出一个合法响应。
//
// 错误优先：c.Error() 非空且尚未写过响应时落地成标准错误响应体。
// 已经在响应阶段被改写过的（例如 CORS 合并后的 401）不会被覆盖。
func finalize(c *core.RequestContext) {
	if c.CloseConn {
		return
	}
	if c.Response != nil {
		return
	}
	if err := c.Error(); err != nil {
		core.DefaultErrorResponse(c, err)
		return
	}
	c.SetResponse(http.StatusOK, http.Header{}, nil)
}

// 编译期确认：Chain 是可用的 http.Handler。
var _ http.Handler = (*Chain)(nil)
