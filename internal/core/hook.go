package core

import (
	"fmt"
	"sort"
)

// HookPoint 是请求生命周期中的回调点。
//
// 与 BFE 的 callback 点一一对应，只是去掉了连接级的 Accept/Handshake/Finish：
// 连接生命周期在 http.Server 手里，网关拿不到这些时机。
type HookPoint uint8

const (
	// HookBeforeRoute 请求已解析，尚未确定租户与路由。
	HookBeforeRoute HookPoint = iota + 1
	// HookFoundRoute 租户已确定，路由尚未匹配。
	HookFoundRoute
	// HookAfterRoute 路由与集群已确定，尚未转发。
	HookAfterRoute
	// HookForward 每一次向后端发起尝试之前。
	HookForward
	// HookReadResponse 已拿到下游响应（或上游失败）。
	HookReadResponse
	// HookRequestFinish 请求结束，无论成败都执行。
	HookRequestFinish
)

// HookPointCount 是回溯点数上限。
const HookPointCount = int(HookRequestFinish) + 1

var hookNames = [HookPointCount]string{
	HookBeforeRoute:   "before_route",
	HookFoundRoute:    "found_route",
	HookAfterRoute:    "after_route",
	HookForward:       "forward",
	HookReadResponse:  "read_response",
	HookRequestFinish: "request_finish",
}

func (p HookPoint) String() string {
	if int(p) < 0 || int(p) >= HookPointCount {
		return "unknown"
	}
	return hookNames[p]
}

// Action 是 handler 的返回值，控制链的推进方式。
//
// 与 BFE 的返回码一致，但零值刻意取 ActionGoOn：漏写 return 时退化成
// "继续执行"，而不是像 BFE 的 Finish=0 那样静默中断整条链。
type Action uint8

const (
	// ActionGoOn 继续执行同回调点的下一个 handler。
	ActionGoOn Action = iota
	// ActionRespond c.Response 已就绪，跳过后续请求阶段。
	ActionRespond
	// ActionFinish 异常终止，由 HookReadResponse 依据 c.Error() 生成响应。
	ActionFinish
	// ActionClose 直接断连，不写响应。
	ActionClose
)

func (a Action) String() string {
	switch a {
	case ActionGoOn:
		return "go_on"
	case ActionRespond:
		return "respond"
	case ActionFinish:
		return "finish"
	case ActionClose:
		return "close"
	default:
		return "unknown"
	}
}

// Handler 是挂在某个回调点上的处理函数。
type Handler func(*RequestContext) Action

// Module 是插件单元。
//
// 一个模块可以在多个回调点注册 handler（例如 CORS 同时挂在请求前与响应前），
// 这是把"阶段"换成"回调点"后最直接的收益。
type Module interface {
	Name() string
	Init(*Callbacks) error
}

// NamedHandler 保留 handler 的来源，便于 Topology 输出与装配期查错。
type NamedHandler struct {
	Module  string
	Name    string
	Order   int
	Handler Handler
}

func (n NamedHandler) String() string { return n.Module + "." + n.Name }

// Callbacks 收集各回调点注册的 handler。
type Callbacks struct {
	Points [HookPointCount][]NamedHandler
}

// NewCallbacks 创建空回调表。
func NewCallbacks() *Callbacks {
	return &Callbacks{}
}

// Add 在指定回调点注册 handler。
//
// order 决定同回调点内的执行顺序（小的先执行）；模块注册顺序不影响执行顺序。
// 同一「模块.handler」重复注册会 panic：装配期的错误不应该留到线上。
func (c *Callbacks) Add(point HookPoint, order int, module, name string, h Handler) {
	if h == nil {
		panic(fmt.Sprintf("core: %s.%s 注册了 nil handler", module, name))
	}
	if int(point) >= HookPointCount {
		panic(fmt.Sprintf("core: 非法回调点 %d", point))
	}
	key := module + "." + name
	for _, existing := range c.Points[point] {
		if existing.Module+"."+existing.Name == key {
			panic(fmt.Sprintf("core: %s 在 %s 上重复注册", key, point))
		}
	}
	c.Points[point] = append(c.Points[point], NamedHandler{
		Module:  module,
		Name:    name,
		Order:   order,
		Handler: h,
	})
}

// Topology 按回调点展开已注册的 handler，用于启动日志与排障。
func (c *Callbacks) Topology() map[string][]string {
	out := make(map[string][]string, HookPointCount)
	for point := HookBeforeRoute; point <= HookRequestFinish; point++ {
		names := make([]string, 0, len(c.Points[point]))
		for _, h := range c.Points[point] {
			names = append(names, h.String())
		}
		out[point.String()] = names
	}
	return out
}

// Sort 把每个回调点按 order 稳定排序，保证执行顺序与注册顺序解耦。
func (c *Callbacks) Sort() {
	for point := HookBeforeRoute; point <= HookRequestFinish; point++ {
		handlers := c.Points[point]
		sort.SliceStable(handlers, func(i, j int) bool {
			return handlers[i].Order < handlers[j].Order
		})
	}
}
