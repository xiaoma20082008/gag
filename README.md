# gag

An AI/API gateway written by Go

## 项目结构

```text
gag/
├── cmd/
│   └── main/
│       └── main.go                  # 程序主入口：解析参数、信号转 ctx、退出码（其余在 internal/gateway）
├── configs/
│   └── gateway.yaml                 # 生产环境配置文件
├── internal/                        # 私有业务逻辑和核心模块（外部不可导入）
│   ├── admin/                       # 管理模块（控制平面）
│   │   ├── admin.go                 # 管理端口（AdminServer）
│   │   ├── apis.go                  # 管理接口（探针 / 指标 / 配置发布与回滚）
│   │   └── store.go                 # 配置版本、发布、回滚、订阅（Watch）
│   ├── config/                      # 配置模块
│   │   ├── client.go                # ConfigClient 接口（配置从哪来）+ 来源→快照流水线
│   │   ├── file.go                  # 文件来源（轮询内容变化）
│   │   ├── nacos.go                 # nacos 配置中心来源
│   │   ├── loader.go                # 解析 / 默认值 / 校验（YAML + JSON 一条流水线）
│   │   ├── types.go                 # 声明式 DSL 模型
│   │   ├── validate.go              # 语义校验
│   │   └── snapshot.go              # 不可变快照（数据面唯一输入）
│   ├── gateway/                     # 进程装配：双端口 + 热加载 + 优雅停机
│   │   ├── gateway.go               # Gateway 生命周期（New / Start / Wait / Run / Close）
│   │   ├── watch.go                 # 两路配置变更订阅（控制面发布 + 来源变化）
│   │   ├── listen.go                # 地址拆分、并行停机
│   │   └── build.go                 # 构建标识（-ldflags 注入）
│   ├── core/                        # 核心实现：请求生命周期契约
│   │   ├── core.go                  # RequestContext、统一错误响应、请求体读取
│   │   ├── hook.go                  # 回调点框架（HookPoint / Action / Callbacks / Module）
│   │   └── model.go                 # 领域模型（Instance / Decision / RequestMeta）与分流
│   ├── discovery/                   # 服务发现
│   │   ├── discovery.go             # 统一 DiscoveryClient 抽象接口 (nacos, consul 等)
│   │   ├── static.go                # 静态实例来源（本地开发 / 单机部署）
│   │   ├── nacos.go                 # nacos 实现
│   │   └── resolver.go              # 调用期解析：分流 → 取实例 → 负载均衡
│   ├── lb/                          # 负载均衡与重试机制
│   │   ├── balancer.go              # 轮询 (RoundRobin)、加权、一致性Hash等策略
│   │   └── retry.go                 # 故障转移与重试逻辑
│   ├── llm/                         # AI 大模型适配层
│   │   ├── client.go                # 统一 AI 抽象接口 (OpenAI, Anthropic, Gemini 等)
│   │   ├── openai.go                # OpenAI 协议适配器
│   │   ├── anthropic.go             # Anthropic 协议适配器
│   │   └── stream.go                # SSE（Server-Sent Events）流式响应透传处理
│   ├── middleware/                  # 中间件链
│   │   ├── request_id.go            # 请求标识 / traceparent 透传
│   │   ├── body_limit.go            # 请求体大小限制
│   │   ├── ip.go                    # IP 黑白名单
│   │   ├── auth.go                  # 鉴权与租户管理（API Key / JWT）
│   │   ├── validate.go              # 请求校验
│   │   ├── ratelimit.go             # 基于 Redis / Token Bucket 的限流
│   │   ├── breaker.go               # 熔断器
│   │   ├── cors.go
│   │   ├── logger.go                # 请求日志记录
│   │   ├── metrics.go               # 指标埋点
│   │   ├── tracing.go               # 链路追踪收尾
│   │   ├── recovery.go              # Panic 恢复
│   │   ├── set.go                   # 路由级策略集（限流/熔断/重试/认证）
│   │   └── state.go                 # 中间件访问运行时状态的收敛入口
│   ├── proxy/                       # 核心反向代理与转发逻辑
│   │   ├── handler.go               # 一次下游尝试：解析实例 → 选节点 → 转发 → 判重试
│   │   ├── transformer.go           # 请求/响应头改写、hop-by-hop 清理、Token 统计埋点
│   │   ├── invoker.go               # 下游协议调用器抽象
│   │   ├── http_invoker.go          # HTTP 协议实现
│   │   └── dubbo_invoker.go         # Dubbo 扩展点（桩）
│   ├── router/                      # 路由定义与执行引擎
│   │   ├── router.go                # 装配治理链、内建钩子（租户/路由解析）
│   │   ├── chain.go                 # 回调点执行：顺序、短路、panic 兜底
│   │   ├── table.go                 # 路由表（租户 → 路由 → 服务，纯内存匹配）
│   │   ├── match.go                 # 路径/方法/头匹配
│   │   └── rewrite.go               # 路径改写
│   ├── server/                      # 代理模块（数据平面）
│   │   ├── server.go                # 数据端口(Server)
│   │   ├── serve.go                 # 监听、accept、TCP keepalive
│   │   ├── handler.go               # 请求进入治理链的适配层
│   │   └── state.go                 # 运行时状态与热加载原子替换
│   ├── storage/                     # 数据持久化与缓存
│   │   ├── redis.go                 # Redis 客户端连接与缓存
│   │   └── mysql.go                 # mysql连接
│   └── telemetry/                   # 可观测性统一管理中心
│       ├── logging/                 # 日志
│       ├── metrics/                 # 指标统计
│       └── tracing/                 # 链路追踪
├── pkg/                             # 可供外部引用的公用库
│   ├── errors/                      # 统一错误码定义
│   │   └── errors.go
│   └── mk/                          # DSL封装（词法/语法/类型检查/字节码/VM）
│       ├── ast.go
│       └── ...
├── api/                             # API 定义与契约
│   └── openapi.yaml                 # OpenAPI / Swagger 规范文档（管理端口）
├── scripts/                         # 运维与构建脚本
│   ├── build.sh                     # 交叉编译 + gofmt/vet/test 门禁
│   └── deploy.sh                    # 起依赖 / 构建镜像 / 状态与日志
├── build/                           # 容器化与部署配置
│   ├── Dockerfile                   # 多阶段构建，运行期 distroless/static
│   └── docker-compose.yaml          # nacos / redis / mysql 本地编排
├── go.mod                           # Go 模块依赖声明
├── go.sum                           # 依赖校验和
├── LICENSE                          # Apache License Version 2.0
└── README.md                        # 项目说明文档
```

## 快速开始

```bash
go run ./cmd/main -c configs/gateway.yaml -l info   # 数据面 :8080，管理面 127.0.0.1:9090
curl -H 'X-API-Key: dev-key-1' http://127.0.0.1:8080/v1/orders/1
curl http://127.0.0.1:9090/readyz
```

```bash
./scripts/build.sh -c     # gofmt + vet + test，产物在 dist/gag
./scripts/deploy.sh up    # 本地依赖（nacos / redis / mysql）
```

## 文档

| 文档 | 内容 |
| --- | --- |
| [internal/README.md](internal/README.md) | 包结构与依赖方向、请求链路、双端口、热加载 |
| [docs/README.md](docs/README.md) | 总体设计 |
| [docs/MODULES.md](docs/MODULES.md) | 回调点框架与治理中间件 |
| [docs/ROUTING.md](docs/ROUTING.md) | 路由匹配与查找链 |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | 部署与运维 |
| [api/openapi.yaml](api/openapi.yaml) | 管理端口 API 契约 |
