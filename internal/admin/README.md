# 管理端口实现（控制平面）

管理面是**进程内的第二个 HTTP 服务**，与数据面（`internal/server`）共享进程、不共享端口。
它回答三件事：进程还活着吗（`/healthz`）、能不能接流量（`/readyz`）、现在生效的是哪版配置
（`/admin/status`），并提供改变数据面行为的唯一入口（`POST /admin/config`、`POST /admin/rollback`）。

契约见 [`api/openapi.yaml`](../../api/openapi.yaml)。

## 文件

| 文件 | 职责 |
| --- | --- |
| `admin.go` | `AdminServer` 生命周期（`Initialize` / `Start` / `Close` / `Addr`）、gin 引擎、路由注册辅助方法 |
| `apis.go` | 7 个端点、鉴权中间件 `guard`、回环地址判定 |
| `store.go` | 配置版本：`Publish` / `Current` / `History` / `Rollback` / `Watch` |

## 端点

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/healthz` | 否 | 进程存活，恒 200 |
| GET | `/readyz` | 否 | `Deps.Ready` 返回错误时 503（配置未装载） |
| GET | `/metrics` | 否 | Prometheus 抓取，`Deps.Metrics` 为 nil 时返回占位文本 |
| GET | `/admin/status` | 是 | 当前修订号、版本、hash、发布人、历史条数 + `Deps.Info()` 的内容 |
| GET | `/admin/config` | 是 | 当前配置（YAML 原文） |
| POST | `/admin/config` | 是 | 发布新配置，body 为 YAML；解析或校验失败返回 400，数据面不受影响 |
| POST | `/admin/rollback?id=N` | 是 | 回滚到指定修订，等价于把历史版本重新发布一次 |

探针与指标刻意不鉴权：K8s 的 kubelet 和 Prometheus 不会带 token，让它们走鉴权
只会逼着运维在集群里散播凭据。它们只暴露"活着/就绪"和已聚合的指标，没有配置内容。

## 鉴权

`server.admin_token` 非空时校验 `Authorization: Bearer <token>`，用
`subtle.ConstantTimeCompare` 比较（避免按字节提前返回泄漏长度与内容）。

`admin_token` 为空时**只允许回环地址访问**，而不是"无鉴权放行"——默认配置漏填 token
不该等于把发布接口开给公网。判定基于 `RemoteAddr` 的 IP 部分，不信任 `X-Forwarded-For`：
后者是客户端可控的，用它做鉴权等于没有鉴权。

## 为什么管理面用 gin、数据面不用

数据面的路由语义由配置驱动（`/v1/orders/*`、优先级、灰度分流），框架自带的路由表、
404/405/301 语义会和网关自己的匹配规则打架；管理面只有 7 条固定路径，用 gin 换来
参数绑定与分组路由的便利，收益明显。两个服务的生命周期接口保持一致
（`New` / `Start` / `Close` / `Addr`），心智模型不用切换。

## 热加载链路

```
POST /admin/config
  → config.LoaderFor(Content-Type).Decode   ← 非法配置在这里就被挡住，数据面不受影响
  → Store.PublishSnapshot（生成修订号，通知订阅者）
  → server.Gag.Apply（构建 State + 路由表 + 治理链，成功后原子替换）
```

`Store.Watch` 返回一个 channel，`cmd/main` 里有一个 goroutine 消费它并调 `Apply`。
两个刻意的设计：

- **channel 不关闭**：关闭 channel 需要和发布方同步，而发布方可能是任意 goroutine；
  退出条件由消费方从 `ctx` 感知。`pushLatest` 用非阻塞发送 + 丢弃旧值，
  保证慢消费者不会阻塞发布路径。
- **发布失败不影响旧配置**：`Apply` 失败时 `cmd/main` 记一条 error 日志并保留当前配置，
  `/readyz` 不会因此变红——宁可跑旧版本，也不要半可用。

## 测试

`admin_test.go` 覆盖：探针与指标的免鉴权、`Ready` 失败时 503、有/无 token 两种鉴权路径
（含非回环地址被拒）、发布后 `Current` 与历史的变化、回滚到指定修订、
以及未配置 Store 时 `/admin/config` 返回 503 而不是 panic。
