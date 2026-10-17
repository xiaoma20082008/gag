# 模块与请求处理设计（参考 BFE）

本文描述把网关的请求处理从「四阶段 filter 链」演进为「回调点 + 模块」的方案，机制参考
[BFE](https://github.com/bfenetworks/bfe) 的 module framework。

状态：**已实施**。`internal/gateway` 提供回调点框架（HookPoint / Action / Callbacks / Chain），
`internal/modules` 承载 12 个治理模块；旧的 `gateway.Phase` / `gateway.Filter` / `Pipeline`
与 `internal/filters` 包已删除。

实施过程中对设计做的三处修正，均以「不改变对外行为」为前提：

| # | 设计 | 实施 | 原因 |
| --- | --- | --- | --- |
| 1 | 未定义重试退避的归属 | `Hooks.Backoff` 由框架调用，等待期间可被客户端断连取消 | 重试循环搬进框架后，退避若留在模块里会出现第二份实现；但「等多久」是策略，仍由 `mod_upstream` 提供 |
| 2 | `Retryable(c, attempt)` | `Retryable(c, err, attempt)` | 中间次失败不能写进 `c.Error()`：一旦重试成功，错误会残留成「200 + error_code」。本次错误由框架显式传入 |
| 3 | `HookReadResponse` 仅在成功路径执行 | `Respond` / `Finish` / panic 之后同样执行，且执行前先 `finalize` | 否则 CORS 头合并、hop-by-hop 清理只覆盖成功路径，被拒绝的请求会漏掉这些头 |

`internal/gateway/chain_test.go` 覆盖返回码矩阵与上述三条语义，`internal/modules/modules_test.go`
用内存 `State` 跑完整链做行为回归。

## 1. 为什么要改

现有实现能跑，但扩展成本在变高：

| 问题 | 具体表现 |
| --- | --- |
| 阶段粒度太粗 | `auth` / `validate` / `rate_limit` / `downstream` 全挤在 `invoke`；转发重试的**每一次尝试**对链上其它模块不可见 |
| 一个 filter 只能属于一个阶段 | CORS 需要在请求前写头、响应后合并头，只能拆成两个 filter 并用 `attr` 传递中间状态 |
| 用 error 表达控制流 | `ErrAbort` 是哨兵错误，想新增「直接返回响应」「重定向」就得改 `Pipeline.run` |
| 顺序写死在装配代码 | 新增模块必须修改 `filters.Build` 里的注册顺序，顺序本身是隐式契约 |
| 跨阶段能力被塞进 post | trace、metrics、access log 都堆在 post，只因为那里是唯一"一定会执行"的地方 |

## 2. BFE 的机制（事实核对）

### 2.1 九个回调点

BFE 的 `bfe_module/bfe_callback.go` 定义了 9 个回调点，请求流程（`bfe_server/reverseproxy.go`）按顺序触发：

```
HandleAccept → HandleHandshake
  → HandleBeforeLocation      （路由前）
  → findProduct               （核心逻辑，不是模块）
  → HandleFoundProduct        （找到 product 后）
  → findCluster               （核心逻辑）
  → HandleAfterLocation       （确定 cluster 后）
  → HandleForward             （clusterInvoke 内部，每次后端尝试都触发）
  → HandleReadResponse        （拿到响应后）
  → HandleRequestFinish       （ServeHTTP 返回后，在 FinishReq 里）
  → HandleFinish              （连接结束）
```

### 2.2 模块自己注册 handler

```go
type BfeModule interface {
    Name() string
    Init(cbs *BfeCallbacks, whs *web_monitor.WebHandlers, cr string) error
}
```

模块在 `Init` 里调用 `cbs.AddFilter(point, fn)` 把自己挂到回调点上。框架维护全局
`moduleMap` 与有序的 `modulesAll`；`BfeModules.RegisterModule(name)` 决定哪些模块启用，
**初始化按注册顺序，而不是配置文件顺序**。

### 2.3 返回码就是协议

`bfe_module/bfe_handler_list.go`：

```go
const (
    BfeHandlerFinish   = iota // 0：回包后关闭连接（并强制 500）
    BfeHandlerGoOn            // 1：继续下一个 handler
    BfeHandlerRedirect        // 2：跳转
    BfeHandlerResponse        // 3：使用模块提供的响应，跳过后端
    BfeHandlerClose           // 4：直接断连，不发送数据
)
```

`HandlerList` 逐个调用 handler，返回值不是 `GoOn` 就中断。`ServeHTTP` 对每个请求阶段
的返回值做同一套解释，因此模块不需要知道自己在链中的位置。

### 2.4 一个常被误解的点

BFE 的 handler list 是**全局**的，不是 per-product。per-product 的行为来自模块**自己的配置**
（例如 `mod_rewrite` 按 product 组织规则），框架层并不按 product 选链。

## 3. 目标设计

### 3.1 回调点

我们不需要连接级钩子（连接生命周期在 `http.Server` 手里），保留 6 个：

| 回调点 | 时机 | 典型用途 | 当前对应 |
| --- | --- | --- | --- |
| `HookBeforeRoute` | 请求已解析，尚未确定租户/路由 | request id、体积限制、IP 黑白名单、CORS 写头 | `pre.*` |
| `HookFoundRoute` | 租户已确定、路由尚未匹配 | 按租户改写策略、打灰度标记 | 新增 |
| `HookAfterRoute` | 路由与集群已确定，尚未转发 | 认证、参数校验、限流 | `invoke.auth/validate/rate_limit` |
| `HookForward` | 每次向后端发起尝试前 | 熔断判定、重试、下游调用 | `invoke.downstream` |
| `HookReadResponse` | 已拿到下游响应 | 响应规范化、CORS 合并、头改写 | `post.response` |
| `HookRequestFinish` | 请求结束（无论成败） | 访问日志、指标、trace 收尾 | `post.access_log/metrics/trace` |

租户解析与路由查找属于框架内建（等价 BFE 的 `findProduct` / `findCluster`），不做成模块：
它们决定后续所有模块看到的上下文，做成模块会让「哪些模块能看到路由结果」变得依赖顺序。

`HookFoundRoute` 目前没有模块使用，保留它是为了和 BFE 的 `findProduct → HandleFoundProduct`
对齐：后续出现「按租户改写策略」这类需求时，不需要再动框架。

### 3.2 接口

```go
// internal/gateway/hook.go
type HookPoint uint8

const (
    _ HookPoint = iota + 1
    HookBeforeRoute 
    HookFoundRoute
    HookAfterRoute
    HookForward
    HookReadResponse
    HookRequestFinish
)

// Action 是 handler 的返回值，语义与 BFE 一致，但零值取"继续"。
type Action uint8

const (
    ActionGoOn      Action = iota // 0：继续执行同回调点的下一个 handler
    ActionRespond                 // c.Response 已就绪，跳过后续请求阶段
    ActionFinish                  // 异常终止：由 ReadResponse 依据 c.Error() 生成响应
    ActionClose                   // 直接断连（仅用于流式/半开场景）
)

type Handler func(*RequestContext) Action

// Module 是插件单元：一个模块可以在多个回调点注册 handler。
type Module interface {
    Name() string
    Init(*Callbacks) error
}

// Callbacks 收集各回调点的 handler。
type Callbacks struct {
    points [HookRequestFinish + 1][]namedHandler
}

// Add 注册 handler；order 决定同回调点内的执行顺序（小的先执行）。
// 同名模块 + 同名 handler 重复注册直接 panic：装配期的错误不应该留到线上。
func (c *Callbacks) Add(point HookPoint, order int, name string, h Handler)
```

模块示例（CORS 同时在两个回调点注册，这正是拆分阶段做不到的）：

```go
type CORSModule struct{ cfg *config.CORS }

func (m *CORSModule) Name() string { return "mod_cors" }

func (m *CORSModule) Init(cbs *gateway.Callbacks) error {
    cbs.Add(gateway.HookBeforeRoute, 40, "cors.request", m.onRequest)
    cbs.Add(gateway.HookReadResponse, 50, "cors.response", m.onResponse)
    return nil
}
```

### 3.3 执行器

```go
type Chain struct {
    points [HookRequestFinish + 1][]Handler
}

// Fire 执行某个回调点，返回第一个非 GoOn 的结果。
func (ch *Chain) Fire(point HookPoint, c *RequestContext) Action
```

框架内建钩子（`internal/gateway/chain.go` 的 `Hooks`）与回调点的分工：
回调点可被模块无限扩展，钩子决定「后续模块能看到什么上下文」，因此必须内建。

```go
type Hooks struct {
    ResolveTenant func(*RequestContext) error            // 租户查找（≈ BFE findProduct）
    ResolveRoute  func(*RequestContext) error            // 路由查找（≈ BFE findCluster）
    Forward       func(*RequestContext) error            // 一次下游尝试（≈ clusterInvoke）
    MaxAttempts   func(*RequestContext) int              // 最多几次
    Retryable     func(c *RequestContext, err error, attempt int) bool
    Backoff       func(c *RequestContext, attempt int) time.Duration
}
```

重试循环由 `Chain.forward` 持有：每一次尝试前触发 `HookForward`，
因此熔断之外的模块也能按 attempt 做判断——这是旧 `invoke.downstream`
把循环藏在单个 filter 内部做不到的。

语义（对应现有 `Pipeline.run`）：

| Action | 同回调点剩余 handler | 后续请求阶段 | HookReadResponse | HookRequestFinish |
| --- | --- | --- | --- | --- |
| `ActionGoOn` | 继续 | 继续 | 执行 | 执行 |
| `ActionRespond` | 跳过 | 跳过 | 执行 | 执行 |
| `ActionFinish` | 跳过 | 跳过 | 执行 | 执行 |
| `ActionClose` | 跳过 | 跳过 | 跳过 | 执行 |

补充约定：

- `HookReadResponse` 对 `Respond` 与 `Finish` 都执行：前者需要合并 CORS 头、清理 hop-by-hop 头；
  后者需要把 `c.Error()` 落成 401/429/502 的响应体。
- `Respond` 与 `Finish` 的执行路径相同，区别在语义：前者表示 `c.Response` 已就绪（如 CORS 预检 204），
  后者表示治理拒绝或异常（如认证失败）。保留两个码是为了让访问日志与审计能区分「业务短路」和「治理拒绝」。
- `ActionClose` 跳过 `HookReadResponse`（连接已经不在了），但 `HookRequestFinish` 仍执行，用于记录客户端中断。
- `HookRequestFinish` **无条件执行**且忽略返回值——日志、指标、trace 不能因为请求失败而丢。
- handler panic 被捕获，转成 `CodeInternal` 的 500，然后继续执行 `HookRequestFinish`。

与现有语义的对应：

| 现有语义 | 新实现 |
| --- | --- |
| fail-fast | 非 finish 点 `Action != GoOn` 短路 |
| post 必执行 | `HookRequestFinish` 必执行 |
| `ErrAbort`（CORS 预检） | `ActionRespond` |
| filter panic → 500 | handler panic → 500 + 继续 finish |

### 3.4 模块清单

现有 14 个 filter 中，租户解析与路由查找内建为框架逻辑，其余 12 个变成模块：

| 模块 | 回调点（order） | 职责 | 来源 |
| --- | --- | --- | --- |
| `mod_request_id` | BeforeRoute(10) | 生成/透传 request id、traceparent | `pre.request_id` |
| `mod_body_limit` | BeforeRoute(20) | 请求体大小限制 | `pre.body_limit` |
| `mod_ip` | BeforeRoute(30) | IP 黑白名单 | `pre.ip_filter` |
| `mod_cors` | BeforeRoute(40) / ReadResponse(50) | 预检短路 + 响应头合并 | `pre.cors` + `post.response` 的一部分 |
| `mod_auth` | AfterRoute(10) | API Key / JWT、scope 校验 | `invoke.auth` |
| `mod_validate` | AfterRoute(20) | Content-Type、必填头/查询/字段 | `invoke.validate` |
| `mod_ratelimit` | AfterRoute(30) | 令牌桶 / 滑动窗口 | `invoke.rate_limit` |
| `mod_upstream` | Forward(10) | 熔断、重试、下游调用 | `invoke.downstream` |
| `mod_response` | ReadResponse(10) | 错误响应生成、hop-by-hop 头清理、头改写 | `post.response` |
| `mod_access_log` | RequestFinish(20) | 访问日志 | `post.access_log` |
| `mod_metrics` | RequestFinish(30) | 请求数、耗时、限流/熔断计数 | `post.metrics` |
| `mod_trace` | RequestFinish(40) | span 收尾 | `post.trace` |

`mod_upstream` 内部的每次重试都调用 `Fire(HookForward, c)`，因此熔断计数、重试次数对
后续模块可见——这是现有 `invoke.downstream` 一个 filter 内部循环做不到的。

### 3.5 装配

```go
func Build(opts Options) (*gateway.Chain, error) {
    cbs := gateway.NewCallbacks()
    modules := []gateway.Module{
        NewRequestID(),
        NewBodyLimit(opts.MaxBody),
        NewIP(opts.IPAllow, opts.IPDeny),
        NewCORS(opts.CORS),
        NewAuth(opts.State),
        NewValidate(opts.State),
        NewRateLimit(opts.State),
        upstream,
        NewResponse(opts.State),
        NewAccessLog(opts.Logger),
        NewMetrics(opts.Metrics),
        NewTrace(),
    }
    for _, m := range modules {
        // 未配置策略的模块构造时返回 nil，这里直接跳过，
        // 而不是塞一个"什么都不做"的模块占位。
        if m == nil {
            continue
        }
        if err := m.Init(cbs); err != nil {
            return nil, fmt.Errorf("%s: %w", m.Name(), err)
        }
    }
    return gateway.NewChain(cbs, gateway.Hooks{...}), nil
}
```

模块列表的顺序不再影响执行顺序，顺序只由 `Add` 的 `order` 决定。
注意 `NewBodyLimit` / `NewIP` / `NewCORS` / `NewAccessLog` / `NewMetrics`
在无配置时返回 nil：Go 的接口零值陷阱要求返回类型是 `gateway.Module` 且显式 `return nil`，
否则 `m == nil` 判断会因类型化的 nil 指针而失效。

### 3.6 可选增强：按路由开关模块（非 BFE 能力）

BFE 没有按 product 选链的能力。如果后续需要「某个路由关闭限流」，建议走运行期跳过而不是
编译期生成子链：

```yaml
routes:
  - id: order.get
    modules:
      disable: [mod_ratelimit]
```

实现方式：`RequestContext` 记录当前路由的禁用集合，`Fire` 时跳过。编译期按路由生成子链
会让热加载的内存占用随路由数线性增长，不划算。**本期不做**，仅记录方向。

## 4. 迁移步骤

已按下面的顺序完成，每一步都以现有测试为回归基线（401 / 404 / 413 / 429 / 502、CORS 预检、
traceparent 透传、panic→500、post 必执行、热加载策略复用）。

| 步骤 | 内容 | 状态 |
| --- | --- | --- |
| 1 | 新增 `gateway/hook.go` + `chain.go` + 单测 | 完成 |
| 2 | `pre.*` 迁到 `HookBeforeRoute`，`mod_cors` 同时挂响应段 | 完成 |
| 3 | `invoke.*` 迁到 `HookAfterRoute` + `HookForward`，重试循环进框架 | 完成 |
| 4 | `post.*` 迁到 `HookReadResponse` + `HookRequestFinish` | 完成 |
| 5 | 删除 `Phase` / `Filter` / `ErrAbort` / `Pipeline` 与 `internal/filters`，更新 README | 完成 |

最终拓扑（`go run ./cmd/main` 启动日志的 `topology` 字段也可看到）：

```
before_route    mod_request_id.request.id → mod_body_limit.body.limit → mod_ip.ip.filter → mod_cors.cors.request
found_route     （无模块）
after_route     mod_auth.auth.authenticate → mod_validate.validate.request → mod_ratelimit.ratelimit.allow
forward         mod_upstream.upstream.invoke
read_response   mod_response.response.normalize → mod_cors.cors.response
request_finish  mod_access_log.access.log → mod_metrics.metrics.record → mod_trace.trace.end
```

## 4.1 迁移中发现并修掉的三个行为缺陷

这些是旧实现里就存在、迁移时才暴露的问题，已在新实现中修正：

| 缺陷 | 旧表现 | 新表现 |
| --- | --- | --- |
| 熔断把 5xx 记成成功 | `Breaker.Record(err == nil)` 只看传输错误，下游稳定返回 500 时熔断器永远不打开 | `err == nil && StatusCode < 500` 才算成功 |
| hop-by-hop 清理顺序 | 先删 `Connection` 再读它点名要删的头，被点名的头会泄漏到下游 | 先读出点名清单，再删 hop-by-hop 头 |
| 非幂等方法重试 | 依赖路由级 `max_attempts`，POST 也可能被重试 | `maxAttempts` 与 `retryable` 双重校验幂等性 |

## 5. 明确不照搬的部分

- **连接级钩子**：`HandleAccept` / `HandleHandshake` / `HandleFinish` 依赖连接生命周期，
  我们把它交给了 `http.Server`，拿不到这些时机。需要时用 `ConnState` 回调替代。
- **`BfeHandlerFinish` 强制 500**：错误响应已经由 `gateway.Error` 统一表达，模块不应该
  隐式改写状态码。
- **返回码零值是 Finish**：BFE 的 `Finish = 0` 意味着「漏写 return」会静默中断整条链。
  我们用 `ActionGoOn = 0`，让零值退化成最安全的行为。
- **全局 handler list 不支持裁剪**：先接受；确有需要时按 §3.6 做运行期跳过。

## 6. 风险与验证（实施结果）

| 风险 | 应对 | 结果 |
| --- | --- | --- |
| 顺序回归 | `order` 冲突在装配期报错；同一回调点内保持稳定排序 | `chain_test` 用逆序注册验证执行顺序只由 order 决定 |
| 语义回归 | 保留端到端测试作为基线，`chain_test` 覆盖返回码矩阵 | 401/403/404/400/413/429/502/CORS/trace 透传/panic→500 全部保留并通过 |
| 热加载 | `Chain` 与 `State` 一起原子替换，`server.Apply` 接口不变 | `server_test` 的策略复用与 Handler 稳定性用例通过；真实二进制冒烟复验 |
| 性能 | handler 切片只读、无每请求分配；相比现有 4 次链遍历，6 个回调点只是多几次循环 | 未做基准测试；每请求新增的分配只有 `NewRequestContext`（与旧实现相同） |
| 新增缺陷 | 迁移时顺带修掉熔断误判、hop-by-hop 泄漏、非幂等重试 | 见 §4.1，均有对应回归用例 |

## 7. 参考

- `bfe_module/bfe_module.go`：模块注册与初始化顺序
- `bfe_module/bfe_callback.go`：9 个回调点定义
- `bfe_module/bfe_handler_list.go`：HandlerList 与返回码
- `bfe_server/reverseproxy.go`：`ServeHTTP` 中的回调顺序与返回码解释
