# 路由查找与调度设计

## 1. 总体目标

路由层是 API/AI Gateway 请求目标服务的关键，它负责从请求解析到一个确定的服务、服务集群和实例。

本设计遵循以下分层查找模型：

Tenant -> Service -> Cluster -> Instance

其中：

- Tenant：租户/业务域，如 order、inventory、ai、internal
- Service：逻辑服务，如 order-service、model-infer
- Cluster：服务部署集群，如 order-service-hz、order-service-sh、model-infer-canary
- Instance：Cluster 内的实际节点，如 10.0.0.1:1234

---

## 2. 路由设计原则

1. 以租户为边界
   - 所有 Route 与 Service 都应绑定租户契约，避免跨租户流量混淆。
   - 对多租户模式要求支持 tenant context 注入、租户参数校验以及租户级 ACL。

2. 以服务为核心
   - 路由最终命中的是逻辑服务，不直接绑定单实例。
   - Service 层负责协议、超时、重试、熔断策略以及版本选择。

3. 以 cluster 为伸缩单元
   - Cluster 是服务的部署层，负责地域、版本和机器池的归类。
   - Cluster 可按 AZ / region / version / lane 分层管理，便于做灰度、蓝绿和扩容。

4. 以 instance 为执行节点
   - Instance 是最终执行的连接端点。
   - Instance 的选择由 LB 算法、健康检查和权重决定。

5. 允许可观测性与回滚
   - 路由选择必须输出 trace tag（tenant/service/cluster/version）
   - 路由决策应可审计、可灰度、可回滚

---

## 3. 路由查找流程

### 3.1 入口请求

请求进入网关后，Pre 阶段先注入统一上下文，包括：

- trace_id
- request_id
- tenant
- client_id
- route_key
- source_app / source_ip

随后进入 Route 阶段，系统根据请求内容从高维度到低维度进行查找。

### 3.2 层级查找步骤

#### Step 1：Tenant 解析

Tenant 来源优先级建议：

1. 认证 Token / JWT 中声明的 tenant
2. 请求头中如 x-tenant / x-tenant-id
3. 域名、path 参数中的租户标识
4. 默认租户（如 public / default）

规范要求：

- Tenant 必须存在且具备合法性
- 对未授权租户，不允许进入 Service Lookup
- 对 tenant 变更执行统一审计日志

#### Step 2：Service 解析

在 tenant 空间内查找 route 匹配规则，确定具体 Service：

- 通过 path、method、header、host、query 匹配
- 通过 rule 表达式匹配（DSL，见 §6.3）
- 若存在多个服务匹配，则按 priority、结构特异性裁定

Service 允许实现：

- 静态 route（path/methods/host/headers/query）
- 动态 route（控制平面下发，热替换整份快照）
- 规则 route（rule 表达式，表达静态字段写不出的条件）
- 版本 route（v1/v2/canary，用 rule + priority 指向不同 cluster）

#### Step 3：Cluster 获取

在 Service 下找到对应 cluster：

- e.g. order-service -> order-service-hz / order-service-sh / order-service-canary

Cluster 选择策略：

- 默认按 region / zone / lane 选择
- 若 zone 健康不佳，可 fallback 到同 region 的其他 cluster
- 灰度分流在 route 阶段就已决定目标 cluster：命中灰度规则的路由直接写
  灰度 cluster，不在这里再做二次分流（见 §8）

#### Step 4：Instance 选择

Cluster 内按实例选择器选择具体实例：

- weight
- least connection
- round robin
- consistent hash（适合稳定 key/会话绑定场景）
- latency-aware routing（优先低延迟节点）

选择前应做：

- 健康检查状态过滤
- 负载阈值过滤
- 重试候选池筛选
- 异常实例剔除

---

## 4. 路由数据结构设计

### 4.1 Tenant 表

租户是实际业务隔离的基本边界。参考字段：

- id
- name
- namespace
- owner
- status（active / disabled / archived）
- region / zone
- default_route_version
- created_at / updated_at

### 4.2 Service 表

Service 表象征逻辑服务。参考字段：

- id
- tenant_id
- name
- protocol（http / grpc / tcp / ai）
- version
- target_cluster_policy
- timeout_ms
- max_retries
- load_balancer
- status
- metadata

### 4.3 Cluster 表

Cluster 表象征部署单元或实例池。参考字段：

- id
- service_id
- name
- region
- zone
- version
- health_status
- weight
- lb_policy
- max_concurrency
- fallback_cluster_id
- status

### 4.4 Instance 表

Instance 表象征实际后端单点。参考字段：

- id
- cluster_id
- host
- port
- ip
- weight
- status（healthy / unhealthy / draining / removed）
- last_heartbeat
- latency_ms
- error_rate
- connection_count
- version

---

## 5. 路由查找索引设计

为降低 Route 阶段的查找成本，建议采用多级索引。思路如下：

1. 先索引租户
   - tenant_id -> tenant config

2. 再索引 service
   - tenant_id + route path + method -> service_id

3. 再索引 cluster
   - service_id + version + region -> cluster_id[]

4. 最终索引实例
   - cluster_id -> instance[]

推荐的索引类型：

- path trie / radix tree：适合 path 规则匹配
- hash map：适合 tenant/service lookup
- segment tree or sorted list：适合 region/zone 权重排序
- memory cache：缓存最热 route 结果，TTL 可配

---

## 6. 路由查询算法

### 6.1 匹配顺序

查找分两个阶段，中间隔着"认证"这道墙：

**阶段一（route 期，`router.Table.ResolveRoute`，纯内存）**

1. tenant 识别（未带租户则回落 `server.default_tenant`）
2. 在租户的路由表里按排序逐条试匹配：
   - 静态字段快速筛选：host → methods → path → headers → query
   - 静态字段全过之后才求值 rule（DSL）
3. 命中即返回 `Decision{Service, RouteID, Route, ServiceCfg}`

排序规则（建表时算好，查找时不再比较）：

- 先比 `priority`，大的在前
- `priority` 相同时比结构特异性得分：
  精确路径（10000+长度）> 前缀路径（1000+长度）> 仅 rule（0 基分）
- 同分时保持配置书写顺序（`sort.SliceStable`）

**阶段二（invoke 期，`discovery.Resolver.Resolve`，每次尝试都重解析）**

4. cluster 过滤（region / version / 健康）
5. instance 选择（lb：round_robin / random / least_conn / consistent_hash）
6. 失败则按 retry 策略重试——**重试会重新走 4-5**，因此换节点是天然行为，
   不需要额外的 failover 逻辑

之所以拆成两段：认证、限流、熔断都按路由配置，必须先知道路由才知道该用
哪套策略；而实例列表是运行时数据，进不了启动期的路由表，且重试必须能换节点。

### 6.2 伪代码示例

```text
// 阶段一：route 期，内存查表
func ResolveRoute(meta) (Decision, error) {
    tenant := meta.Tenant or defaultTenant
    routes := table[tenant]                  // 已按 priority/score 排好序
    if routes == nil { return tenant_not_found }

    env := buildDSLEnv(meta)                 // 每个请求构造一次，租户下复用
    for r := range routes {
        if !r.matchStatic(meta) { continue } // 廉价字段先筛
        if r.rule != nil {
            ok, err := r.rule.Match(env)
            if err != nil { return route_eval_failed }  // 500，不是 404
            if !ok { continue }
        }
        return Decision{Service: r.svc, Cluster: r.cluster, Route: r}
    }
    return route_not_found
}

// 阶段二：invoke 期，每次尝试都重新解析
func Resolve(decision, attempt) (Instance, error) {
    nodes := discover(tenant, service, decision.Cluster)
    healthy := filter(nodes, healthy)
    if len(healthy) == 0 { return unavailable }   // 503
    return lb.Select(healthy, key)
}
```

### 6.3 路由规则 DSL

`rule` 是一个布尔表达式，**在配置加载期编译一次**，请求期只做求值。

编译期做的事（不是请求期）：

- 词法 / 语法分析
- 静态类型检查（字符串和整数比较直接报错，不做隐式转换）
- `matches()` 的正则 `regexp.Compile`
- `cidr()` 的 `netip.ParsePrefix`

请求期做的事：AST 求值 + map 查找。没有正则编译，没有前缀解析。

**环境变量**

| 写法 | 含义 |
| --- | --- |
| `http.method` / `http.path` / `http.host` | 请求行与 Host |
| `http.client_ip` | 客户端 IP（已过 IP 中间件，可能为空） |
| `http.tenant` | 租户，路由前由 `X-Tenant` / 默认租户定好，**可用** |
| `http.header["X-Foo"]` | 请求头，大小写不敏感 |
| `http.query["a"]` | 查询参数 |
| `http.cookie["c"]` | Cookie |
| `http.user` / `http.region` / `vars[...]` | **路由期恒为空**，见下 |

> **求值时机陷阱**：路由规则在 `found_route` 求值，认证在 `after_route` 才跑。
> 所以 `http.user`、`http.region` 在路由期永远是空串，
> 依赖它们的规则**静默不命中**且不报错。要按身份分流，用认证前就存在的
> 请求头（如 `X-Canary`），或把判断挪到 `after_route` 的模块里。
>
> `vars[...]` 同理：`config.InboundRequest.VarsMapping` 还没接进查找链，
> `Route` 上也没有对应字段。语法先支持，等 VarsMapping 落地后不用改语法。

**谓词**

| 谓词 | 说明 |
| --- | --- |
| `startsWith(s, sub)` / `endsWith(s, sub)` / `contains(s, sub)` | 子串判断 |
| `matches(s, 're')` | 正则，第二参数必须是字符串字面量 |
| `cidr(ip, '10.0.0.0/8')` | 网段，第二参数必须是字符串字面量 |
| `in(s, 'a', 'b', ...)` | 枚举，等价于一长串 `\|\|` |

**运算符**：`!` > `&&` > `||` > 比较（`==` `!=` `>` `>=` `<` `<=`）。
不支持链式比较（`a == b == c` 编译报错）。

**示例**

```yaml
- id: order.get.canary
  rule: 'http.header["X-Canary"] == "true"'
  methods: [GET]
  priority: 10
  cluster: order-service-canary

- id: order.get.internal
  rule: 'matches(http.path, "^/v1/orders/[0-9]+/(audit|replay)$") && cidr(http.client_ip, "10.0.0.0/8")'
  methods: [GET]
  priority: 5
  cluster: order-service-hz
```

**设计取舍**

- **不做隐式类型转换**：`http.path == 123` 是编译错误而不是永远 false。
  一条永远为假的规则在线上表现成"路由不存在"，排查成本极高。
- **求值失败返回 500**（`core.ErrRouteEval`），不是 404。坏规则不该
  伪装成"路由没配"。
- **`cidr()` 遇到脏 IP 返回 false 而不报错**：请求数据不可信，
  但这不是配置错误。
- **规则只写条件，不写动作**：目标 cluster 是路由的静态字段。
  规则里没有"改写 path""设置 header"这类副作用，那些属于 transformer。

实现见 `internal/dsl/`；`pkg/mk` 是另一套面向网关脚本的运行时，
其 AST 解释器目前是桩（`MKRuntime.Eval` 返回 `ErrRuntimeUnavailable`），
不能用来求值路由规则。

### 6.4 配置热更新

路由表是 `config.Snapshot` 的派生物，跟随快照整体替换：

1. 新配置先过 `Validate`（含 `dsl.Check` 逐条编译规则）
2. 通过后构建新 `Table`（第二次编译，防止绕过加载期校验）
3. 原子替换指针，旧表由 GC 回收

因此不存在"新配置生效一半"的中间态，也不需要读写锁：
查找路径上没有写操作，读到的一定是某一份完整配置。

版本控制：

- `Snapshot.Version()` 由配置里的 `version` 字段或内容哈希前 12 位给出
- `Snapshot.Hash()` 是内容的 sha256，用于判断"这次下发到底变没变"
- 规则编译失败的下发在步骤 1 就被拒绝，不会落地

---

## 7. cluster 与实例状态管理

### 7.1 健康检查

健康检查是路由稳定性的关键。故障检测策略如下：

- TCP connect 健康检查
- HTTP / gRPC readiness 检查
- 业务级探针（例如 model-service 健康接口）

健康状态建议分层：

- healthy
- suspect
- unhealthy
- draining
- removed

### 7.2 负载均衡

Cluster 方案中，支持：

- weight round robin
- least connection
- consistent hash
- latency-based selection

对于 AI 或长尾任务场景，还可以在 Cluster 层采用：

- worker 队列长度考量
- model capacity-aware routing
- 优先分配空闲节点

### 7.3 容错策略

当 selected instance 不可用时：

1. 尝试同 cluster 其他 instance
2. 尝试 fallback cluster
3. 若仍失败，进入 circuit breaker / service degrade
4. 返回降级内容或 503 兜底

---

## 8. 灰度与流量切分

对于灰度发布，route 选择时不仅关注目标 service，还需考虑流量规则。

### 8.1 情形

- 按 tenant 分流：例如 A 账号走 v1，B 账号走 v2
- 按 header 分流：例如 x-canary=true
- 按 cookie 分流：例如 beta user
- 按 percentage 分流：例如 5% 走 canary
- 按 region 分流：例如 华北区走 v1，华南区走 v2

### 8.2 实现方式

分流分两层，**路由级只做"确定性"分流，百分比分流交给 lb**：

**路由级（确定性）**：同一 service 下配两条路由，灰度那条写 rule + priority。

```yaml
routes:
  - id: order.get.canary
    rule: 'http.header["X-Canary"] == "true"'
    methods: [GET]
    priority: 10          # 压过下面的前缀路由
    cluster: order-service-canary

  - id: order.get
    path: /v1/orders/*
    methods: [GET]
    cluster: order-service-hz
```

为什么不靠结构分：仅 rule 的路由没有 path，结构分是 0 基分，
不写 priority 就永远排在 `/v1/orders/*` 后面，灰度不会生效。
这是最容易踩的坑，`TestPriorityBeatsStructuralScore` 专门守着它。

**lb 级（百分比）**：把两个版本的实例放进同一个 cluster，
用实例权重分流（nacos 的 `weight` 或静态配置的 `weight`）。
`internal/lb` 的 round_robin / random / least_conn 都按权重归一。

**没有路由级 split**：一条请求只能落到一个 cluster，
`Route.Cluster` 是单值。要表达"5% 走 canary"就在 lb 层给实例配权重，
而不是在路由层做加权随机——后者会让同一请求在重试时可能换版本。

必须保证：

- 记录分流命中信息（route id / priority / cluster / version）
- 在 metrics 与 trace 中展示区分（按 route id 打 label）
- 支持回滚：把灰度路由删掉或把 priority 归零即回到全量旧版本

---

## 9. 多租户与隔离

多租户设计是 API Gateway 的典型需求。

### 9.1 设计要求

- tenant 与路由配置隔离
- tenant 与 service namespace 隔离
- tenant 与 cluster 权限隔离
- tenant 级别使用专有缓存键或 prefix

### 9.2 常见问题

- 误配 route：route 规则存在误触发跨租户访问
- 资源争用：不同租户共用相同 cluster 导致资源扰动
- 健康状态不真实：一个租户的故障被错误反映到全局

解决方式：

- 路由查找必须先确定 tenant
- cluster 配置支持 tenant scopes
- 做 tenant 权限与标签审计

---

## 10. 观测与诊断要求

Route 阶段必须具备强观测性，建议打点：

- route_lookup_total
- route_lookup_latency_ms
- route_match_fail_total
- route_cluster_select_total
- route_instance_select_total
- route_fallback_total
- route_tenant_not_found_total

日志字段建议：

- trace_id
- tenant
- service
- cluster
- instance
- route_rule_id
- decision_reason
- fallback_reason

这样多租户场景中的排查会大幅简化。

---

## 11. 安全要求

- route 规则和 cluster 选择必须受控，不允许任意配置覆盖
- 控制平面必须对路由变更进行审批和审计
- 所有路由配置应在热更新前做校验，避免误分流
- 访问路径中不要泄漏后台 cluster 信息给未授权客户端

---

## 12. 运行时回滚策略

当路由错误、cluster 不可用或灰度异常时，必须具备：

1. route rule version rollback
2. cluster fallback
3. instance drain and remove
4. config rollback from control plane

推荐回滚策略：

- 先回滚灰度分流
- 再回滚 service->cluster mapping
- 再考虑对 instance 做��池中摘除

---

## 13. 扩展

后续可以继续在以下方向扩展：

- 引入 service mesh sidecar 对接
- 支持更细粒度金丝雀发布
- 增加缓存一致性策略和 route snapshot
- 支持 cluster 维度智能调度（基于延迟、error rate、queue length）
- 在 AI 场景增加路由优先级（如模型实例按并发与 GPU 资源分配）
