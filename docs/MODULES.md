# 模块与请求处理设计（参考 BFE）

本文描述把网关的请求处理从「四阶段 filter 链」演进为「回调点 + 模块」的方案，机制参考
[BFE](https://github.com/bfenetworks/bfe) 的 module framework。

状态：**已实施**，并已按 `internal/README.md` 的分层重排过目录。当前归属：

| 关注点 | 包 | 内容 |
| --- | --- | --- |
| 生命周期契约 | `internal/core` | `HookPoint` / `Action` / `Callbacks` / `Module` / `RequestContext` / 领域模型 |
| 执行器 | `internal/router` | `Chain`（跑回调点、短路、兜 panic、无条件收尾）、`Table`、`Build`（装配） |
| 治理中间件 | `internal/middleware` | 12 个中间件 + 策略集（限流/熔断/认证/校验） |
| 下游调用 | `internal/proxy` | `mw_upstream`（一次尝试）、`mw_transformer`（响应规范化） |

旧的 `gateway.Phase` / `gateway.Filter` / `Pipeline` 与 `internal/filters` 包已删除；
`internal/gateway` 与 `internal/modules` 也已按职责拆散（契约进 `core`、执行器进 `router`、
中间件进 `middleware`、转发进 `proxy`），不再存在。

查找链从 `internal/routing` 迁出，拆成两段执行：
路由期（`found_route` → `router.Table.ResolveRoute`）只做内存匹配，
调用期（`forward` → `discovery.Resolver.Resolve`）才定集群、取实例、做负载均衡。
实例来源由 `discovery.DiscoveryClient` 抽象，`discovery.FromConfig` 按 `type` 构造具体来源：
`static` 读配置清单，`nacos` 已接入（`internal/discovery/nacos.go`），`consul` 会直接拒绝装配。
详见 §4.2。

实施过程中对设计做的三处修正，均以「不改变对外行为」为前提：

| # | 设计 | 实施 | 原因 |
| --- | --- | --- | --- |
| 1 | 未定义重试退避的归属 | `Hooks.Backoff` 由框架调用，等待期间可被客户端断连取消 | 重试循环搬进框架后，退避若留在模块里会出现第二份实现；但「等多久」是策略，仍由 `mw_upstream` 提供 |
| 2 | `Retryable(c, attempt)` | `Retryable(c, err, attempt)` | 中间次失败不能写进 `c.Error()`：一旦重试成功，错误会残留成「200 + error_code」。本次错误由框架显式传入 |
| 3 | `HookReadResponse` 仅在成功路径执行 | `Respond` / `Finish` / panic 之后同样执行，且执行前先 `finalize` | 否则 CORS 头合并、hop-by-hop 清理只覆盖成功路径，被拒绝的请求会漏掉这些头 |

`internal/router/chain_test.go` 覆盖返回码矩阵与上述三条语义，`internal/router/pipeline_test.go`
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
// internal/core/hook.go
type HookPoint uint8

const (
    HookBeforeRoute HookPoint = iota + 1
    HookFoundRoute
    HookAfterRoute
    HookForward
    HookReadResponse
    HookRequestFinish
)

const HookPointCount = int(HookRequestFinish) + 1

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

// NamedHandler 保留 handler 的来源，Topology 靠它输出 module.name。
type NamedHandler struct {
    Module  string
    Name    string
    Order   int
    Handler Handler
}

// Callbacks 收集各回调点的 handler。
type Callbacks struct {
    Points [HookPointCount][]NamedHandler
}

// Add 注册 handler；order 决定同回调点内的执行顺序（小的先执行）。
// module + name 唯一：同一「模块.handler」重复注册直接 panic，
// 非法回调点或 nil handler 同样 panic——装配期的错误不应该留到线上。
func (c *Callbacks) Add(point HookPoint, order int, module, name string, h Handler)

// Sort 按 order 稳定排序；Topology 按回调点展开成 map[string][]string 供启动日志使用。
func (c *Callbacks) Sort()
func (c *Callbacks) Topology() map[string][]string
```

`module` 与 `name` 分开传，是因为排障时需要能按模块分组（`Topology` 输出的是
`mw_cors.cors.request` 这种全名）；把它拼成一个字符串就再也拆不回来了。

模块示例（CORS 同时在两个回调点注册，这正是拆分阶段做不到的）：

```go
type CORS struct{ cfg *config.CORS }

func (m *CORS) Name() string { return "mw_cors" }

func (m *CORS) Init(cbs *core.Callbacks) error {
    cbs.Add(core.HookBeforeRoute, 40, "mw_cors", "cors.request", m.onRequest)
    cbs.Add(core.HookReadResponse, 50, "mw_cors", "cors.response", m.onResponse)
    return nil
}
```

### 3.3 执行器

```go
// internal/router/chain.go
type Chain struct {
    points [core.HookPointCount][]core.NamedHandler
    hooks  Hooks
}

// Fire 执行某个回调点，返回第一个非 GoOn 的结果。
func (ch *Chain) Fire(point core.HookPoint, c *core.RequestContext) core.Action

// 内部入口：跑完六个回调点 + 内建钩子 + 重试循环，兜住所有 panic。
func (ch *Chain) runStages(c *core.RequestContext) core.Action
```

框架内建钩子（`internal/router/chain.go` 的 `Hooks`）与回调点的分工：
回调点可被模块无限扩展，钩子决定「后续模块能看到什么上下文」，因此必须内建。

```go
type Hooks struct {
    ResolveTenant func(*core.RequestContext) error            // 租户查找（≈ BFE findProduct）
    ResolveRoute  func(*core.RequestContext) error            // 路由查找（≈ BFE findCluster）
    Forward       func(*core.RequestContext) error            // 一次下游尝试（≈ clusterInvoke）
    MaxAttempts   func(*core.RequestContext) int              // 最多几次
    Retryable     func(c *core.RequestContext, err error, attempt int) bool
    Backoff       func(c *core.RequestContext, attempt int) time.Duration
}
```

重试循环由 `Chain.forward` 持有：每一次尝试前触发 `HookForward`，再调 `Hooks.Forward`。
`mw_upstream` 就是挂在 `HookForward` 上的那一个 handler，它只负责「一次尝试」，
循环本身在框架里——因此熔断、审计之外的模块也能按 attempt 做判断，
这是旧 `invoke.downstream` 把循环藏在单个 filter 内部做不到的。

只有最终失败才写 `c.Error()`：中间次失败是「还在重试」，写进去会让重试成功后
留下「200 + error_code」的脏状态。

语义（对应旧 `Pipeline.run`）：

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

旧的 14 个 filter 中，租户解析与路由查找内建为框架逻辑，其余 12 个变成中间件。
按现在的目录，它们分居两个包：治理类在 `internal/middleware`，转发类在 `internal/proxy`。

| 模块 | 所在包 | 回调点（order） | 职责 | 来源 |
| --- | --- | --- | --- | --- |
| `mw_request_id` | middleware | BeforeRoute(10) | 生成/透传 request id、traceparent | `pre.request_id` |
| `mw_body_limit` | middleware | BeforeRoute(20) | 请求体大小限制 | `pre.body_limit` |
| `mw_ip` | middleware | BeforeRoute(30) | IP 黑白名单 | `pre.ip_filter` |
| `mw_cors` | middleware | BeforeRoute(40) / ReadResponse(50) | 预检短路 + 响应头合并 | `pre.cors` + `post.response` 的一部分 |
| `mw_auth` | middleware | AfterRoute(10) | API Key / JWT、scope 校验，成功后填 `c.Subject` | `invoke.auth` |
| `mw_validate` | middleware | AfterRoute(20) | Content-Type、必填头/查询/字段 | `invoke.validate` |
| `mw_ratelimit` | middleware | AfterRoute(30) | 令牌桶 / 滑动窗口 | `invoke.rate_limit` |
| `mw_upstream` | proxy | Forward(10) | 熔断判定、解析实例、发请求（一次尝试） | `invoke.downstream` |
| `mw_transformer` | proxy | ReadResponse(10) | 错误响应生成、hop-by-hop 头清理、头改写 | `post.response` |
| `mw_access_log` | middleware | RequestFinish(20) | 访问日志 | `post.access_log` |
| `mw_metrics` | middleware | RequestFinish(30) | 请求数、耗时、限流/熔断计数 | `post.metrics` |
| `mw_trace` | middleware | RequestFinish(40) | span 收尾 | `post.trace` |

重试循环在 `Chain.forward` 里，每次尝试前 `Fire(HookForward, c)`，`mw_upstream` 只处理其中一次。
因此「这是第几次尝试」对后续 handler 可见——旧 `invoke.downstream` 一个 filter 内部循环做不到。

### 3.5 装配

```go
// internal/router/router.go
func Build(opts Options) (*Chain, error) {
    upstream := proxy.NewUpstream(opts.State, opts.Invokers, discovery.NewResolver(opts.Source))

    mods := []core.Module{
        middleware.NewRequestID(),
        middleware.NewBodyLimit(opts.MaxBody),
        middleware.NewIP(opts.IPAllow, opts.IPDeny),
        middleware.NewCORS(opts.CORS),
        middleware.NewAuth(opts.State),
        middleware.NewValidate(opts.State),
        middleware.NewRateLimit(opts.State),
        upstream,
        proxy.NewTransformer(opts.State),
        middleware.NewAccessLog(opts.Logger),
        middleware.NewMetrics(opts.Metrics),
        middleware.NewTrace(),
    }

    cbs := core.NewCallbacks()
    for _, m := range mods {
        // 未配置策略的模块构造时返回 nil，这里直接跳过，
        // 而不是塞一个"什么都不做"的模块占位。
        if m == nil {
            continue
        }
        if err := m.Init(cbs); err != nil {
            return nil, fmt.Errorf("router: %s 初始化失败: %w", m.Name(), err)
        }
    }
    return NewChain(cbs, Hooks{
        ResolveTenant: TenantResolver(opts.State),
        ResolveRoute:  RouteResolver(opts.State),
        Forward:       upstream.Attempt,
        MaxAttempts:   upstream.MaxAttempts,
        Retryable:     upstream.Retryable,
        Backoff:       upstream.Backoff,
    }), nil
}
```

模块列表的顺序不影响执行顺序，顺序只由 `Add` 的 `order` 决定。
注意 `NewBodyLimit` / `NewIP` / `NewCORS` / `NewAccessLog` / `NewMetrics`
在无配置时返回 nil：Go 的接口零值陷阱要求返回类型是 `core.Module` 且显式 `return nil`，
否则 `m == nil` 判断会因类型化的 nil 指针而失效。

`Options` 里另外三项与模块无关但决定装配结果：`Invokers`（协议调用器注册表）、
`Source`（实例来源，交给 `discovery.NewResolver`）、`State`（路由表 + 策略集）。
`State` 是唯一必填项，`router` 只依赖它的 `Table()` / `Policy()` / `Snapshot()` 三个方法，
因此测试可以塞一个内存实现跑完整链。

### 3.6 可选增强：按路由开关模块（非 BFE 能力）

BFE 没有按 product 选链的能力。如果后续需要「某个路由关闭限流」，建议走运行期跳过而不是
编译期生成子链：

```yaml
routes:
  - id: order.get
    modules:
      disable: [mw_ratelimit]
```

实现方式：`RequestContext` 记录当前路由的禁用集合，`Fire` 时跳过。编译期按路由生成子链
会让热加载的内存占用随路由数线性增长，不划算。**本期不做**，仅记录方向。

## 4. 迁移步骤

已按下面的顺序完成，每一步都以现有测试为回归基线（401 / 404 / 413 / 429 / 502、CORS 预检、
traceparent 透传、panic→500、post 必执行、热加载策略复用）。

| 步骤 | 内容 | 状态 |
| --- | --- | --- |
| 1 | 新增 `core/hook.go` + `router/chain.go` + 单测 | 完成 |
| 2 | `pre.*` 迁到 `HookBeforeRoute`，`mw_cors` 同时挂响应段 | 完成 |
| 3 | `invoke.*` 迁到 `HookAfterRoute` + `HookForward`，重试循环进框架 | 完成 |
| 4 | `post.*` 迁到 `HookReadResponse` + `HookRequestFinish` | 完成 |
| 5 | 删除 `Phase` / `Filter` / `ErrAbort` / `Pipeline` 与 `internal/filters`，更新 README | 完成 |
| 6 | 按 `internal/README.md` 分层重排：`gateway`→`core`+`router`，`modules`→`middleware`+`proxy`，`policy`→`middleware`，`observability/*`→`telemetry/*`，`controlplane`→`admin` | 完成 |

最终拓扑（`go run ./cmd/main` 启动日志的 `topology` 字段也可看到）：

```
before_route    mw_request_id.request.id → mw_body_limit.body.limit → mw_ip.ip.filter → mw_cors.cors.request
found_route     （无模块）
after_route     mw_auth.auth.authenticate → mw_validate.validate.request → mw_ratelimit.ratelimit.allow
forward         mw_upstream.upstream.invoke
read_response   mw_transformer.response.normalize → mw_cors.cors.response
request_finish  mw_access_log.access.log → mw_metrics.metrics.record → mw_trace.trace.end
```

`router.Topology(chain)` 输出的就是这张表，`router/pipeline_test.go` 里有一条用例
（`TestTopologyReflectsRegisteredOrder`）盯着它，改名漏改会直接红。

## 4.1 迁移中发现并修掉的三个行为缺陷

这些是旧实现里就存在、迁移时才暴露的问题，已在新实现中修正：

| 缺陷 | 旧表现 | 新表现 |
| --- | --- | --- |
| 熔断把 5xx 记成成功 | `Breaker.Record(err == nil)` 只看传输错误，下游稳定返回 500 时熔断器永远不打开 | `err == nil && StatusCode < 500` 才算成功 |
| hop-by-hop 清理顺序 | 先删 `Connection` 再读它点名要删的头，被点名的头会泄漏到下游 | 先读出点名清单，再删 hop-by-hop 头 |
| 非幂等方法重试 | 依赖路由级 `max_attempts`，POST 也可能被重试 | `maxAttempts` 与 `retryable` 双重校验幂等性 |
| 静态实例逐条覆盖 | — | 归并表是整体替换语义，按条目逐条写进去只会留下最后一条；多实例集群编译时先归并再一次写入 |

### 4.1.1 nacos 适配时踩到的三个 SDK 语义

`internal/discovery/nacos.go` 接入 nacos SDK 时，三个"能编译、行为全错"的点：

| 坑 | 表现 | 处理 |
| --- | --- | --- |
| `HealthyOnly` 默认 false | SDK 按 `host.Healthy == healthy` 过滤，不传 = 只返回**不健康**实例，网关把所有请求打到挂掉的节点上 | 显式传 `HealthyOnly: true` |
| 空列表与查询失败都是 error | SDK 对"实例列表为空"返回空切片+error、对连接失败返回 nil+error；一律当故障会把容量问题报成 502 | 按返回值是否为 nil 区分：空列表按"没有实例"处理（603/503），nil 视为注册中心故障透传（502） |
| 每次构造都拉起后台 goroutine | `NewNamingClient` 每调一次就多一组刷新线程，而热加载会让 State 反复重建 | 客户端按连接参数进程内缓存，同一 nacos 只构造一份；装配期预热实例缓存，首个请求不为冷启动买单 |

## 4.2 查找链：为什么拆成两段、为什么搬进调用期

`Tenant → Service → Cluster → Instance` 四跳里，前三跳来自网关配置，最后一跳来自
nacos/consul 这类注册中心——实例是运行时数据。旧 `internal/routing` 把四跳压在路由期
一次性做完，实例选择代码还是注释掉的（`balancer.Pick` 从未被调用），等于「查了路由、
从未选定实例」。新版拆成两步：

| 阶段 | 做什么 | 不做什么 |
| --- | --- | --- |
| `found_route`（内建 `Hooks.ResolveRoute` → `router.Table.ResolveRoute`） | 租户 → 路由 → 服务；只读内存索引，无 IO | 不定集群、不取实例、不查注册中心 |
| `forward`（`mw_upstream` → `discovery.Resolver.Resolve`） | 灰度分流定集群 → `DiscoveryClient` 取实例 → lb 选择 | 不重复做路由匹配 |

放在调用期的三个理由：

1. **每次尝试重新查一次注册中心**：实例下线后，下一次重试自然落到别的节点，
   而不是死磕同一个连不通的地址（`pipeline_test` 用会"轮换"的来源验证）。
2. **灰度分流需要认证后的身份**：`split.match.users` 按调用方稳定分流，
   而身份要等 `after_route` 的 `mw_auth` 跑完才知道。放在路由期只能按 client_ip 退。
3. **重试换节点是基本常识**：`Hooks.Forward` 每次尝试都由 `Chain.forward` 触发，
   查找链在调用期才能享受到这个时机。

配套改动：

| 改动 | 位置 | 说明 |
| --- | --- | --- |
| `config.Cluster` 去掉 `instances` | `internal/config/types.go` | 实例不再是配置的一部分 |
| 新增 `server.discovery` | `internal/config/types.go` | `type: static` + 静态实例清单（自带 tenant/service/cluster 定位）；`nacos` 已接入（连接参数 + 校验），`consul` 在校验层接受但适配器未实现，`discovery.FromConfig` 对它直接报错，避免静默跑在一个永远没有实例的网关上 |
| `DiscoveryClient` / `StaticDiscoveryClient` | `internal/discovery/{discovery,static}.go` | 实例来源抽象与静态实现；nacos 适配在同目录，按连接参数进程内缓存客户端 |
| `Resolver` | `internal/discovery/resolver.go` | 调用期解析：分流 → 取实例 → 负载均衡；错误在这里归一成 `ErrNoInstances`(503) / `ErrInstanceSource`(502) |
| balancer 缓存 | `internal/discovery/resolver.go` | 按 租户/服务/集群 缓存 `lb.LoadBalancer`：round_robin 的票号、least_conn 的在途计数都有状态，每次新建等于把状态丢掉 |
| 一致性哈希键 | `lb.WithHashKey` | `Select` 签名没有 key 参数，由调用方经 ctx 传入（`c.Subject` 优先，未认证退化为客户端 IP） |
| 一致性哈希算法 | `internal/lb/balancer.go` | 改用 rendezvous（HRW）：节点列表逐次传入，重建哈希环的代价全在热路径上；HRW 无需建环且摘节点时只有受影响 key 改道 |
| least_conn 归还 | `context.AfterFunc` | 接口里没有 `Release`，在途计数随 ctx 结束归还 |
| 重试退避 | `internal/lb/retry.go` | `RetryPolicy` 与负载均衡同包：重试本质是「换个节点再试」，退避参数与 `config.RetryPolicy` 一一对应 |

错误码随之分化：集群下没有实例 → `upstream_unavailable`（503，容量问题）；
下游连不上 → `bad_gateway`（502，故障）；集群既不在配置也不在注册中心 → `bad_gateway`。

## 5. 明确不照搬的部分

- **连接级钩子**：`HandleAccept` / `HandleHandshake` / `HandleFinish` 依赖连接生命周期，
  我们把它交给了 `http.Server`，拿不到这些时机。需要时用 `ConnState` 回调替代。
- **`BfeHandlerFinish` 强制 500**：错误响应已经由 `pkg/errors` 的 `Error` 统一表达，模块不应该
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
