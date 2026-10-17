# DSL 规范（DSL_SPEC.md）

本规范描述用于网关配置的声明式 DSL（概念与示例）。实际 DSL 实现以仓库中 pkg/mk 为准，本文件提供语义说明、校验与使用流程示例。

> 说明：示例采用 YAML 风格用于表达结构化概念，非强制语法；请以 pkg/mk 实际语法为准。

## DSL 设计目标

- 声明式、可读、可版本化、可校验。
- 支持复用（模板/引用）、参数化（env/secret 引用）、热加载与回滚。
- 能表达：Service、Route、Policy/Plugin、Global、Canary/TrafficSplit 等。

## 基本实体

1. Global
   - 描述：全局配置（metrics、tracing、logging、默认超时等）。
   - 字段（示例）：
     - tracing: { enabled: true, sampler: 0.01 }
     - logging: { level: info, json: true, pii_masking: ["password","ssn"] }
     - defaults: { timeout_ms: 5000, retry: {max:0} }

2. Service
   - 描述：下游服务定义（逻辑服务）。
   - 字段（示例）：
     - name: user-service
     - protocol: http|grpc|tcp
     - endpoints: ["http://10.0.0.1:8080","http://10.0.0.2:8080"]
     - timeout_ms, retries, load_balancer: { type: round_robin, weights: [...] }

3. Route
   - 描述：路由匹配规则与策略绑定。
   - 字段（示例）：
     - id/name
     - match:
         - path: /api/v1/users/*
         - method: GET
         - headers: { "x-client": "mobile" }
     - service: user-service
     - filters:    # 按阶段绑定的 filter 列表
         - - name: request-id
             phase: pre
           - name: jwt-auth
             phase: invoke
     - policies: [ auth-default, rate-per-key ]
     - traffic:
         - type: percentage
           percent: 10
           match: { header: { "x-beta": "true" } }

4. Policy / Plugin
   - 描述：可复用的策略定义（鉴权、限流、熔断、transform 等）。
   - 字段（示例）：
     - name: rate-per-key
       type: rate_limit
       params: { scope: api_key, rate: 100, burst: 200, strategy: distributed }

5. Canary / TrafficSplit
   - 描述：灰度控制规则。
   - 字段（示例）：
     - id: canary-v2
       route: /api/v1/model
       rules:
         - percent: 5
           match: { header: { "x-user-group": "beta" } }
         - percent: 0.5
           match: { cookie: { "user_id_mod100": "0-0.5" } }

## Filter/Stages 模型

Filter 分为四个阶段：pre → route → invoke → post。每个 filter 为单一职责，支持以下可配置属性：

- name
- phase: pre|route|invoke|post
- priority: 整数，较小数先执行
- short_circuit: true/false（是否在失败时终止链）
- async: true/false（是否异步执行）
- timeout_ms
- retry: { max, backoff }
- failure_action: fallback|reject|continue

示例（route 中 filters 片段）：

```yaml
filters:
  - name: request-id
    phase: pre
    priority: 10
  - name: path-normalize
    phase: route
  - name: jwt-auth
    phase: invoke
    timeout_ms: 200
  - name: access-log
    phase: post
    async: true
```

## Policy 常见类型（语义说明）

- auth: jwt|api_key|mTLS（支持 introspection 与缓存）
- rate_limit: scope=global/route/key/ip, strategy=local/distributed
- circuit_breaker: sliding_window_size, error_threshold, open_duration_ms
- transform: 请求/响应字段变换、脱敏、结构修改
- observability: tracing_sample_rate, metrics_labels

## 参数校验

- 支持 JSON Schema / Protobuf / 简化表述（required/ types/ pattern/ min/max）
- 支持对 path/query/header/body 的联合校验
- 校验失败返回规范化错误（含错误码、message、trace_id）

## 配置下发与热加载流程

1. 在控制平面提交 DSL 配置（或通过 API/CLI），控制平面做静态校验（schema + 语义）。
2. Dry-run 或预演（可选）：在 subset 数据平面上做 canary 下发并采集指标。若通过则继续。
3. 下发策略：采用增量 diff 流式推送到数据平面（gRPC stream），每个数据平面实例接收并校验、原子替换本地配置。旧配置继续支持活跃请求完成后再回收。
4. 回滚：控制平面提供一键回滚，或自动回滚（若下发后监控到关键指标恶化）。

## 版本与回滚

- 每次配置变更都有版本 ID 与变更元数据（作者、时间、变更理由）。
- 支持快照导出/导入。
- 控制平面保留历史并支持 audit log。

## 示例配置（YAML 风格）

```yaml
global:
  tracing:
    enabled: true
    sampler: 0.01
  logging:
    level: info

services:
  - name: model-service
    protocol: http
    endpoints:
      - http://10.0.10.5:8080
      - http://10.0.10.6:8080

policies:
  - name: auth-jwt
    type: auth
    params:
      jwt_pub_keys: ["/secrets/pubkey1.pem"]

  - name: rate-per-key
    type: rate_limit
    params:
      scope: api_key
      rate: 100
      burst: 200

routes:
  - name: model-infer
    match:
      path: /v1/infer
      method: POST
    service: model-service
    filters:
      - name: request-id
        phase: pre
      - name: body-schema-validate
        phase: invoke
      - name: auth-jwt
        phase: invoke
    policies:
      - rate-per-key
    traffic:
      - type: percentage
        percent: 10
        match:
          header: { "x-beta": "true" }
```

## 安全与 secret 引用

- DSL 中不直接存放明文 secret，支持引用 Vault 或控制平面 secrets 存储（示例： ${secret:jwt_pub_key}）。
- 控制平面负责 secret 的访问控制与审计。

## 校验器

- Schema 校验：在控制平面完成静态校验。
- 语义校验：检查 service/route/policy 的引用完整性、无冲突的路由优先级、策略参数合法性。

## 常见用例

- 灰度发布：通过 traffic.percent + match 组合逐步放量并观察指标。
- 快速回滚：若 canary 阶段出现异常，执行回滚还原上一版 DSL。
- 限流按 Key：为某条 route 绑定 rate_limit 并 scope=api_key。

## 扩展点

- Template/Include：支持定义 template 并在多个 route 中引用。
- Condition language：扩展为更丰富的匹配语言（脚本沙箱或 OPA 规则）。

---

（注：请以仓库中 pkg/mk 的 DSL 实际实现与语法为准；本文档为语义 & 使用指南。）
