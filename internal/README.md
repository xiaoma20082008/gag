# internal 目录说明

本目录是网关的运行时实现，按 [设计文档](../docs/README.md) 的分层组织。
顶层原则：**控制面决定"生效什么"，数据面只负责"执行什么"**，两者之间唯一的契约是不可变的配置快照。

## 包结构

| 包 | 对应设计文档 | 职责 |
| --- | --- | --- |
| `gateway` | §2 §3 | 请求上下文、回调点框架（Hook/Action/Callbacks/Chain）、统一错误模型 |
| `modules` | §3 §4 / MODULES.md | 治理模块（request_id / body_limit / ip / cors / auth / validate / ratelimit / upstream / response / access_log / metrics / trace）与装配 |
| `config` | §9 | 声明式 DSL 模型、YAML 加载、默认值、语义校验、不可变快照 |
| `routing` | §3.2 / ROUTING.md | Tenant → Service → Cluster → Instance 分层查找、灰度分流、健康状态 |
| `lb` | §3.2 | round_robin / random / least_conn / consistent_hash |
| `policy` | §4 | 限流（令牌桶/滑动窗口）、熔断（closed/open/half-open）、重试、认证（API Key / JWT） |
| `observability/metrics` | §5.1 | prometheus 官方客户端：指标注册表、网关标准指标、`/metrics` 抓取端点 |
| `observability/tracing` | §5.2 | W3C traceparent 解析/透传、span 与耗时 |
| `observability/logging` | §5.3 | JSON 结构化日志、异步写入、敏感字段脱敏、访问日志结构 |
| `controlplane` | §8 | 配置版本、发布、回滚、订阅（Watch） |
| `proxy` | §3.3 | 下游协议适配（HTTP 已实现，Dubbo/gRPC 留扩展点） |
| `llm` | §12 | 模型客户端抽象（OpenAI 实现，独立于治理链） |
| `script` | §9 / pkg/mk | mk 策略脚本桥接：编译期校验已可用，执行器待 mk VM 完成 |
| `server` | §7 | 数据平面（Gag Server）：业务端口、运行时状态、热加载原子替换、优雅停机 |
| `admin` | §8 | 控制平面（Gag Admin）：管理端口、探针与指标、配置发布/回滚 |

## 双端口：数据平面与控制平面

进程内启动两个独立的 HTTP 服务，而不是把管理端点挂在业务端口上：

| | Gag Server（`internal/server`） | Gag Admin（`internal/admin`） |
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
  ├─ before_route    request_id / trace → body_limit → ip 黑白名单 → cors 预检
  ├─ (内建)          租户解析          ← 框架内建，非模块
  ├─ found_route     （预留：按租户改写策略）
  ├─ (内建)          路由查找 + 路径改写  ← 框架内建，非模块
  ├─ after_route     auth → validate → rate_limit
  ├─ forward         熔断判定 → 下游尝试 →（可重试，每次尝试都触发 forward）
  ├─ read_response   响应规范化 → cors 响应头
  └─ request_finish  access_log → metrics → trace 收尾
```

模块在自己关心的回调点上注册 handler，顺序由注册时的 `order` 决定而非模块排列顺序。
租户解析与路由查找刻意内建：它们决定后续模块能看到什么上下文，做成模块会让
「哪些模块能看到路由结果」变成注册顺序问题。

失败语义：

- 任一回调点返回非 `GoOn` 即短路；被拒绝的请求仍会执行 `read_response`，
  保证 CORS 头、hop-by-hop 清理对 401/429/502 同样生效。
- `ActionRespond` 表示响应已就绪（如 CORS 预检 204）；`ActionFinish` 表示治理拒绝。
  保留两个码是为了让访问日志能区分「业务短路」与「治理拒绝」。
- `ActionClose` 不写响应直接断连，`read_response` 跳过，但 `request_finish` 仍执行。
- `request_finish` **无条件执行**且逐个兜 panic：一个收尾模块出错不连带其余模块。
- handler panic 被捕获，转成 `internal_error` 的 500，然后继续走完收尾。

更细的设计取舍（为何去掉连接级钩子、为何零值取 `GoOn`）见
[模块与请求处理设计](../docs/MODULES.md)。

## 热加载

```
admin POST /admin/config（管理端口）
  → config.Load（strict 解析 + 校验，非法配置在这里就被挡住）
  → controlplane.Publish（生成修订号，通知订阅者）
  → server.Apply（构建 State + 路由表 + 治理链，成功后原子替换）
```

关键点：

- 编译失败时旧配置继续服务，线上不受影响；`/readyz` 不会因此变红。
- 单条路由策略未变时复用同一个 `policy.Set`，限流令牌与熔断计数得以延续。
- `State` 与 `Chain` 成对原子替换，请求要么看到旧的完整组合，要么看到新的。
- 监听地址变更需要重启：端口在进程启动时绑定，热加载只替换配置内容。

## 运行

```bash
go run ./cmd/main -config configs/gateway.yaml
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

## 已验证的能力

`go test ./...` 覆盖：配置校验与快照稳定性、路由优先级与灰度比例、实例摘除、限流/熔断/重试/认证、
回调点顺序与返回码矩阵（GoOn/Respond/Finish/Close + panic）、端到端转发（401/403/404/413/400/429/502/CORS/trace 透传）、
重试次数与 POST 不重试、熔断快速失败、热加载状态复用、配置发布与回滚，
以及双端口行为（业务端口不响应管理端点、探针不鉴权、管理接口鉴权、发布后数据面即时生效）。

另以真实二进制做过双端口冒烟：未认证 401、下游不可达 502、业务端口不暴露 `/healthz`、
`/admin/config` 发布后旧路由立即 404 且策略集被复用、SIGTERM 后双端口释放且 `exit=0`。

真实链路与单元测试的分工：`internal/gateway` 只测框架语义（顺序、返回码、panic 兜底），
`internal/modules` 用自建的内存 `State` 跑完整链——`modules` 不能被 `server` import
（server 依赖 modules 的装配结果），测试 fixture 因此自带一份路由表与策略集编译。

## 尚未实现（明确的后续项）

- **流式响应**：当前 `gateway.Response.Body` 为 `[]byte`，SSE/大模型流式输出需要增加 `io.Reader` 通道。
- **gRPC / Dubbo**：`proxy` 已留协议接口，尚无具体实现。
- **mk 脚本执行**：编译与诊断已接入，`script.Runtime` 待 mk VM 完成后替换实现。
- **分布式限流**：目前为单实例内存限流，多副本需要接入 Redis 或统一限流服务。
- **TLS / mTLS**：配置模型与入口尚未包含证书管理。
- **OpenTelemetry 导出**：当前只做上下文生成与透传，未接入 OTLP exporter。
- **native histogram / exemplars**：`metrics` 已使用 prometheus 官方客户端，但目前仍是经典直方图分桶，未开启 native histogram 与 trace exemplar。
