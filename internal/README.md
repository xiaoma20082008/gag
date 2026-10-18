# internal 目录说明

本目录是网关的运行时实现，按 [设计文档](../docs/README.md) 的分层组织。
顶层原则：**控制面决定"生效什么"，数据面只负责"执行什么"**，两者之间唯一的契约是不可变的配置快照。

## 包结构

依赖方向自上而下，箭头不允许反向（`core` 不 import 任何 `internal/` 下的业务包，
只依赖 `internal/config` 与 `pkg/errors`，因此不存在环）：

```
pkg/errors  ←  internal/config  ←  internal/core
                                      ↑
        router / middleware / proxy / discovery / lb
                                      ↑
                        internal/server  /  internal/admin
                                      ↑
                                internal/gateway
                                      ↑
                                  cmd/main
```

`internal/gateway` 是装配层：它 import 下面所有包，但没有任何包 import 它
（除 `cmd/main`）。因此它不参与循环依赖的判断，也最适合放"启动顺序"
这种跨包知识。

`internal/script` 只依赖 `pkg/mk`，被 `router` 引用；它同样不在主链上。

| 包 | 对应设计文档 | 职责 |
| --- | --- | --- |
| `core` | §3.2 §3.3 | 请求生命周期契约：`RequestContext`、回调点（`HookPoint`/`Action`/`Callbacks`）、领域模型（`Instance`/`Decision`/`RequestMeta`）、查找错误 |
| `router` | §3 §3.2 | 执行引擎与装配：`Chain`（按 order 跑回调点、兜 panic、无条件收尾）、`Table`（路由表，按 priority + 结构分排序）、`Matcher`（静态字段 + DSL 规则）、`Build`（把中间件挂到回调点上） |
| `script` | ROUTING.md §6.3 | 脚本管理：按名字缓存 `pkg/mk` 编译出的 `Program` 与字节码，供路由规则与请求变换复用。求值（`Eval`）尚在实现中 |
| `middleware` | §4 / MODULES.md | 治理中间件（request_id / body_limit / ip / cors / auth / validate / ratelimit / access_log / metrics / trace / recovery）与策略原语（`Set`、令牌桶、滑动窗口、熔断、API Key / JWT） |
| `proxy` | §3.3 | 下游调用：`Upstream`（一次尝试的完整流程：解析实例 → 选节点 → 发请求 → 判重试）、`Transformer`（响应规范化、头改写、hop-by-hop 清理）、协议调用器（HTTP 已实现，Dubbo 留扩展点） |
| `discovery` | §3.2 | 实例来源：`DiscoveryClient` 抽象 + `static` / `nacos` 实现，以及调用期的 `Resolver`（取路由写死的集群 → 取实例 → 负载均衡，按集群缓存 balancer） |
| `lb` | §3.2 | round_robin / random / least_conn / consistent_hash（HRW，键经 ctx 传入）与 `RetryPolicy`（退避 + 抖动） |
| `config` | §9 | 配置来源 `ConfigClient`（file / nacos）、解析流水线（YAML / JSON 共用一条解析→默认值→校验）、声明式模型、不可变快照 |
| `telemetry/metrics` | §5.1 | prometheus 官方客户端：指标注册表、网关标准指标、`/metrics` 抓取端点 |
| `telemetry/tracing` | §5.2 | W3C traceparent 解析/透传/注入、span 与耗时 |
| `telemetry/logging` | §5.3 | JSON 结构化日志、异步写入、敏感字段脱敏、访问日志结构 |
| `admin` | §8 | 控制平面（Gag Admin）：管理端口、探针与指标、配置版本/发布/回滚/订阅（`Store`） |
| `server` | §7 | 数据平面（Gag Server）：业务端口、运行时状态、热加载原子替换、优雅停机 |
| `gateway` | — | 进程装配：读配置 → 建双端口 → 起热加载 → 等信号 → 并行优雅停机。`cmd/main` 因此只剩参数与信号 |
| `storage` | — | 外部存储客户端（Redis / MySQL），目前只做连接与健康探测 |
| `llm` | §12 | 模型客户端抽象（OpenAI / Anthropic 实现，独立于治理链） |

`pkg/` 下放可被外部引用的东西：`pkg/errors` 是统一的错误码契约（`Code`/`Error`/`AsError`），
`pkg/mk` 是 mk 策略 DSL 的编译器与 VM。它与网关之间的桥接在 `internal/script`
（`pkg/mk/script` 已删除）。

## 双端口：数据平面与控制平面

进程内启动两个独立的 HTTP 服务，而不是把管理端点挂在业务端口上：

| | Data Server（`internal/server`） | Admin Server（`internal/admin`） |
| --- | --- | --- |
| 端口 | `server.listen`（默认 `:8080`） | `server.admin_listen`（默认 `127.0.0.1:9090`） |
| 流量 | 全部业务请求，交给治理链 | 探针、指标、配置发布/回滚 |
| 鉴权 | 由路由策略（API Key / JWT）决定 | `/admin/*` 走 `admin_token`，探针不鉴权 |
| 暴露面 | 对外 | 默认只绑回环 |

这样拆分的理由：

- 管理面可以只绑内网/回环，不必随业务端口一起暴露给公网。
- 配置发布、指标抓取这类请求不会占用业务连接池与超时预算。
- 两个服务生命周期独立：`/readyz` 未就绪时，业务端口仍在监听但不接流量（返回 503）。

两者生命周期接口保持一致（`New` / `Start` / `Close` / `Addr`），便于用同一套心智模型维护；
实现上刻意不同：管理面用 gin 换取路由与参数处理上的便利，业务面不引入任何框架。

业务端口直接跑在标准库上：`net.Listen` 拿到原生 `net.Listener`，`http.Server.Serve`
在其上 accept `net.Conn`，请求立刻进入治理链。这样做的原因：

- 框架的路由表、404/405/301 语义会和网关自己的匹配规则打架（301 还会把 POST 变成 GET）。
- 热加载需要替换整条治理链，框架的 Handler 一旦挂上就不能换；这里用一层原子指针做间接。
- accept 时顺手打开 TCP keepalive，长连接（SSE / 流式推理）的半开连接不必等应用层超时。

配置校验会拒绝 `listen` 与 `admin_listen` 指向同一端口。

## 请求链路

```
client
  │
  ├─ before_route    mw_request_id → mw_body_limit → mw_ip → mw_cors 预检
  ├─ (内建)          租户解析              ← 框架内建，非中间件
  ├─ found_route     （内建）路由查找 + 路径改写
  │                   只做内存匹配：租户 → 路由 → 服务，不碰实例来源
  ├─ after_route     mw_auth → mw_validate → mw_ratelimit
  ├─ forward         熔断判定 → mw_upstream：集群分流 → 注册中心取实例 → 负载均衡 → 下游尝试
  │                   （可重试；每次尝试都重新查一次实例，换个节点再试）
  ├─ read_response   mw_transformer 响应规范化 → mw_cors 响应头
  └─ request_finish  mw_access_log → mw_metrics → mw_trace 收尾
```

每个中间件在自己关心的回调点上注册 handler，顺序由注册时的 `order` 决定而非中间件排列顺序。
租户解析与路由查找刻意内建（`router.Hooks`）：它们决定后续中间件能看到什么上下文，
做成中间件会让「谁能看到路由结果」变成注册顺序问题。

装配入口是 `router.Build`，它把 `Options`（日志、指标、CORS、实例来源、调用器注册表）
翻译成一组 `core.Module`，逐个 `Init(callbacks)` 自注册，最后配上内建钩子。
`Options` 里为 nil 的项直接不挂载——回调点上的空转也是开销。

**查找链为什么要拆成两段。** `Tenant → Service → Cluster → Instance` 里，
前三跳来自网关自己的配置，最后一跳来自 nacos/consul 这类注册中心——实例是运行时数据。
因此路由期（`found_route` → `router.Table.ResolveRoute`）只回答「请求属于哪条路由」：
治理策略是按路由配置的，认证、限流都必须先知道路由；而真正「打到哪」要等调用期
（`forward` → `discovery.Resolver.Resolve`）才能确定：

- 每次尝试重新查一次注册中心，实例下线后下一次重试自然落到别的节点；
- 灰度分流需要按用户稳定哈希，而身份要等认证之后才知道（`c.Subject`）；
- 重试换节点是基本常识，查找链放在调用期才能做到。

实例来源由 `discovery.DiscoveryClient` 抽象，`discovery.FromConfig` 按 `server.discovery` 构造：

| `type` | 行为 |
| --- | --- |
| `static` | 读配置写死的清单（本地开发/单机部署） |
| `nacos` | 租户 → group、服务 → 服务名、集群 → 集群名，只取健康且启用、权重 > 0 的实例；`instance list is empty` 归一成空列表而不是错误 |
| `consul` | 尚未实现，装配期直接报错 |

接注册中心时 `router.Table` 与治理中间件都不需要改；nacos 客户端按连接参数进程内复用
（`newCachedNacos`），热加载不会反复创建 SDK 后台 goroutine，装配期还会预热一遍实例缓存。

失败语义：

- 任一回调点返回非 `GoOn` 即短路；被拒绝的请求仍会执行 `read_response`，
  保证 CORS 头、hop-by-hop 清理对 401/429/502 同样生效。
- 集群下没有实例是 503（容量问题），注册中心不可达是 502（基础设施故障），
  错误码要能区分——运维看监控时的处置动作完全不同。
- `ActionRespond` 表示响应已就绪（如 CORS 预检 204）；`ActionFinish` 表示治理拒绝。
  保留两个码是为了让访问日志能区分「业务短路」与「治理拒绝」。
- `ActionClose` 不写响应直接断连，`read_response` 跳过，但 `request_finish` 仍执行。
- `request_finish` **无条件执行**且逐个兜 panic：一个收尾中间件出错不连带其余。
- handler panic 被捕获，转成 `internal_error` 的 500，然后继续走完收尾。

更细的设计取舍（为何去掉连接级钩子、为何零值取 `GoOn`）见
[模块与请求处理设计](../docs/MODULES.md)。

## 配置来源（ConfigClient）

配置**从哪来**与配置**怎么解析**是两件事，分别由两个接口承担：

```
ConfigClient（file / nacos / apollo）     → 只负责把文档原文取回来
  ↓ config.SnapshotFrom / ParseDocument
ConfigLoader（YAML / JSON）               → 只负责解析成 Config
  ↓ ApplyDefaults + Validate
Snapshot                                   → 数据面唯一输入
```

`ConfigClient` 是整份文档级的抽象，不是字段级：网关配置是一个 22KB 的结构化
对象，按字段 `Get("server.listen")` 既不可读，也没法做原子替换。

| 实现 | key 语义 | 变更检测 |
| --- | --- | --- |
| `FileConfigClient` | 文件路径（空则用构造时的 path） | 轮询，**内容**变化才算（mtime 变不算） |
| `NacosConfigClient` | dataId（空则用配置里的 `data_id`） | SDK 长连接监听，ctx 取消即退订 |

构造 `FileConfigClient` 时就会读一次文件：路径写错立刻失败，不会让网关
拿着一份空配置启动（那会表现成"所有请求 404"，比启动失败难查得多）。

## 热加载

两路变更最终都汇到同一个 `apply`，谁后到谁生效（last write wins）：

```
① 控制面发布
admin POST /admin/config（管理端口）
  → config.ParseDocument（strict 解析 + 校验，非法配置在这里就被挡住）
  → admin.Store.PublishSnapshot（生成修订号，通知订阅者）
  → gateway.apply

② 配置来源自身变化
文件改动 / nacos 推送
  → ConfigClient.OnChange（拿到文档原文）
  → config.SnapshotFrom（同一条解析流水线）
  → gateway.apply
```

关键点：

- 编译失败时旧配置继续服务，线上不受影响；`/readyz` 不会因此变红。
- 内容哈希没变就跳过：只改注释/缩进不重建路由表、不重新预热实例来源。
- 单条路由策略未变时复用同一个 `middleware.Set`（按配置内容的 sha256 比对），
  限流令牌与熔断计数得以延续。
- `State` 与 `Chain` 成对原子替换，请求要么看到旧的完整组合，要么看到新的。
- 监听地址变更需要重启：端口在进程启动时绑定，热加载只替换配置内容。

## 运行

```bash
go run ./cmd/main -c configs/gateway.yaml
```

| 端口 | 端点 | 说明 |
| --- | --- | --- |
| admin | `GET /healthz` | 进程存活 |
| admin | `GET /readyz` | 数据面已装载配置 |
| admin | `GET /metrics` | Prometheus 抓取 |
| admin | `GET /admin/status` | 当前修订、路由数、租户数 |
| admin | `GET /admin/config` | 当前配置（YAML） |
| admin | `POST /admin/config` | 发布新配置（body 为 YAML） |
| admin | `POST /admin/rollback?id=N` | 回滚到指定修订 |
| server | `*` | 全部业务路径，按配置路由 |

`/admin/*` 要求 `server.admin_token`（`Authorization: Bearer <token>`）；
未配置 token 时仅允许回环地址访问，不会退化成"无鉴权放行"。
管理面的契约见 [`api/openapi.yaml`](../api/openapi.yaml)。

构建与部署：

```bash
./scripts/build.sh -c        # gofmt + vet + test，然后构建 dist/gag
./scripts/deploy.sh up       # 起 nacos/redis/mysql，构建镜像
docker build -f build/Dockerfile -t gag:local .
```

## 已验证的能力

`go test ./...` 覆盖：配置校验与快照稳定性、路由字段校验与 priority/结构分排序、
匹配器语义（精确/前缀边界/method/host/header/query）、查找链两段式
（路由期内存匹配 / 调用期集群与实例）、负载均衡策略（权重组、在途计数、一致性哈希稳定性）、
限流/熔断/重试/认证、回调点顺序与返回码矩阵（GoOn/Respond/Finish/Close + panic）、
端到端转发（401/403/404/413/400/429/502/503/CORS/trace 透传）、
重试次数与 POST 不重试、熔断快速失败、热加载状态复用、配置发布与回滚，
以及双端口行为（业务端口不响应管理端点、探针不鉴权、管理接口鉴权、发布后数据面即时生效）。

另以真实二进制做过双端口冒烟（两个真实下游实例）：未认证 401、认证后请求在两个实例间轮询、
`/admin/config` 发布后旧路由立即 404、新路由继续转发且访问日志记录到具体实例、
SIGTERM 后双端口释放且 `exit=0`。

测试的分工：

- `internal/router/chain_test.go` 只测框架语义（顺序、返回码、panic 兜底），不碰 HTTP；
- `internal/router/match_test.go` 只测匹配器（精确/前缀边界/method/host/header/query
  与 score 排序），不启 HTTP 服务，也不依赖配置加载；
- `internal/config/route_test.go` 用一份最小配置逐字段破坏，验证校验错误能定位到具体路由；
- `internal/gateway/listen_test.go` 只测纯函数（地址拆分、构建标识），不占端口；
- `internal/discovery` 测实例来源与 resolver（含 nacos 空列表归一、balancer 复用）；
- `internal/router/pipeline_test.go` 用自建的内存 `State` 跑完整链——`router` 不能
  反向 import `server`（server 依赖 router 的装配结果），测试 fixture 因此自带一份
  路由表、策略集与实例来源。

## 尚未实现（明确的后续项）

- **流式响应**：当前响应体按 `[]byte` 处理，SSE/大模型流式输出需要增加 `io.Reader` 通道。
- **gRPC / Dubbo**：`proxy` 已留 `Invoker` 接口与 dubbo 桩，尚无具体实现。
- **mk 脚本执行**：编译器与诊断已可用（`pkg/mk`），网关侧只接了 `pkg/mk/script` 桥接。
- **分布式限流**：目前为单实例内存限流，多副本需要接入 Redis 或统一限流服务。
- **TLS / mTLS**：配置模型与入口尚未包含证书管理。
- **consul 适配器**：`discovery.DiscoveryClient` 已留好接口，`nacos` 已接入，
  `type: consul` 会在装配期直接报错。
- **主动健康检查**：`Cluster.HealthCheck` 参数已在配置模型中，尚无检查器；
  实例健康目前由注册中心自己回答。
- **OpenTelemetry 导出**：当前只做上下文生成与透传，未接入 OTLP exporter。
- **路由级百分比分流**：`Route.Cluster` 是单值，5% 灰度要在 lb 层用实例权重表达；
  路由级 weighted split 已删除（见 ROUTING.md §8.2）。
- **路由期按身份分流**：认证在 `after_route` 才跑，`http.user` 在路由期恒为空。
- **native histogram / exemplars**：`metrics` 已使用 prometheus 官方客户端，但目前仍是经典直方图分桶。
