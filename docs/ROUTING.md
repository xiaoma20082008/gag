# 路由查找与调度详细设计

## 1. 总体目标

路由层是 API/AI Gateway 对请求做“最终命中目标服务”的关键环节。它负责将一个请求从入口入口条件（path / method / tenant / header / domain / concurrency / version）解析到一个确定的服务、服务集群和实例。

本设计遵循以下分层查找模型：

Tenant -> Service -> Cluster -> Instance

其中：
- Tenant：租户/业务域，如 order、inventory、ai、internal
- Service：逻辑服务，如 order-service、model-infer
- Cluster：服务部署集群，如 order-service-hz、order-service-sh、model-infer-canary
- Instance：Cluster 内的实际节点，如 10.0.0.1:1234

该模型适用于多租户、灰度发布、跨区域部署与高可用路由场景，是微服务 API Gateway 中常用的稳定抽象。

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
- 通过 service alias、route rule、标签匹配
- 若存在多个服务匹配，则按照优先级、namespace、version 规则裁定

Service 允许实现：
- 静态 route
- 动态 route（控制平面下发）
- 模板 route（变量化规则）
- 版本 route（v1/v2/canary）

#### Step 3：Cluster 获取

在 Service 下找到对应 cluster：
- e.g. order-service -> order-service-hz / order-service-sh / order-service-canary

Cluster 选择策略：
- 默认按 region / zone / lane 选择
- 若 zone 健康不佳，可 fallback 到同 region 的其他 cluster
- 若配置存在灰度策略，则在 cluster 选择前应用 traffic split

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

租户是实际业务隔离的基本边界。

建议字段：
- id
- name
- namespace
- owner
- status（active / disabled / archived）
- region / zone
- default_route_version
- created_at / updated_at

### 4.2 Service 表

Service 表象征逻辑服务。推荐字段：
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

Cluster 表象征部署单元或实例池。推荐字段：
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

Instance 表象征实际后端单点。推荐字段：
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

为降低 Route 阶段的查找成本，建议采用多级索引。关键思路：

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

请求路由匹配一般按下面顺序：

1. tenant 识别
2. route rule 匹配
3. service selection
4. cluster filtering
5. instance selection
6. failover / fallback

### 6.2 伪代码示例

```text
func Route(req Request) (Instance, error) {
    tenant := ResolveTenant(req)
    if tenant == nil {
        return error("tenant_not_found")
    }

    service := MatchService(tenant, req)
    if service == nil {
        return error("service_not_found")
    }

    cluster := SelectCluster(service, req)
    if cluster == nil {
        return error("cluster_unavailable")
    }

    instance := SelectInstance(cluster, req)
    if instance == nil {
        if cluster.HasFallback() {
            return SelectFallback(cluster.FallbackCluster, req)
        }
        return error("instance_unavailable")
    }

    return instance, nil
}
```

### 6.3 动态路由规则

动态路由支持以下能力：
- 配置版本切换
- 灰度中 key-based routing
- 可对 cluster 做在运行时切换
- route 规则更新不需重启当前实例

设计建议：
- 支持版本号控制（version / revision）
- 只允许“已校验、不冲突”的配置版本落地
- 路由表更新采用 copy-on-write / atomic swap

---

## 7. cluster 与实例状态管理

### 7.1 健康检查

健康检查是路由稳定性的关键。建议在 instance 层做及时故障检测：
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

Cluster 方案中，建议支持：
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

推荐在 Route 或 cluster 选择前执行 filter：
- route 评估：match rule -> choose candidate cluster
- traffic split：基于 hash / percentage / rule 计算 target cluster

必须保证：
- 记录分流命中信息（route version / split rule）
- 在 metrics 与 trace 中展示区分
- 支持回滚至上一版 cluster

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

## 13. 推荐最终设计结论

我们建议的 core routing model 为：

Tenant -> Service -> Cluster -> Instance

该结构很适合 API/AI Gateway 场景，因为它同时具备：
- 多租户隔离能力
- 逻辑服务与部署单元解耦
- 多版本与灰度支持
- 健康检查 + failover + fallback
- 更好的监控与回滚能力

尤其在高可用和高复杂度的网关环境中，这种分层模型比“单一 path -> instance”的平铺结构更稳定，也更适合长期维护和治理。

---

## 14. 扩展建议

后续可以继续在以下方向扩展：
- 引入 service mesh sidecar 对接
- 支持更细粒度金丝雀发布
- 增加缓存一致性策略和 route snapshot
- 支持 cluster 维度智能调度（基于延迟、error rate、queue length）
- 在 AI 场景增加路由优先级（如模型实例按并发与 GPU 资源分配）

---

## 15. 一句话总结

路由阶段并不是简单的“找一个 IP”，而是一个带租户边界、service 归属、cluster 部署层和 instance 执行层的分层决策系统；它是网关治理能力、灰度发布能力、高可用能力，以及可观测性能力的核心基础设施。

