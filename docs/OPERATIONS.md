# 运维手册

本手册面向平台 SRE 与运维团队，包含部署、健康检查、配置下发与回滚、监控与告警、故障处理与演练指南。

## 部署建议

- 平台：Kubernetes，也支持 VM/裸机多实例部署。
- Data Plane：作为 Deployment，跨多个 AZ 部署多个副本；使用 HPA（基于 CPU / requests_in_flight / 自定义指标）。
- Control Plane：高可用部署（至少 3 节点），持久化配置与审计用数据库（Postgres 等）。

## 健康检查

- liveness：检查进程活性与主循环。
- readiness：检查关键依赖（配置加载、下游重要依赖达序列或缓存初始化）。
- 端点示例：/healthz/live, /healthz/ready

## 配置下发与热加载流程（运维步骤）

1. 在控制平面编辑 DSL 并执行 schema 校验。
2. Dry-run：在控制平面选择 subset 节点进行 canary 下发（例如 5% 的实例）。
3. 监控 canary 指标（error_rate, latency, downstream_errors）30 分钟。
4. 若通过则全量下发；若异常，执行回滚并排查。

自动化路径：CI/CD 将 DSL 变更提交到配置仓库，触发控制平面流程与审批。

## 回滚策略

- 手动回滚：通过控制台选择历史版本并下发。
- 自动回滚：控制平面可配置关键指标阈值，若阈值被跨越自动回滚。
- 记录每次回滚理由与审计信息。

## 监控指标与仪表盘

必备指标：

- requests_total{route,instance,status}
- request_duration_seconds{route,quantile}
- upstream_request_duration_seconds{service}
- requests_in_flight{instance}
- rate_limit_hits{route,scope}
- circuit_breaker_events{route,state}
- config_version_applied{instance}

建议 Dashboard：

- 总流量与 QPS 面板
- 按 route 的 P50/P95/P99 latency
- 熔断/限流触发面板
- 配置下发/回滚与版本面板

## 告警建议（示例）

- Gateway 实例不可用（instance_down > 0）
- route error_rate > 1% 且 QPS > 100
- P99 latency 增长 2x
- 熔断器打开次数急速上升

告警接收：PagerDuty（严重）、Slack（通知）、Email（记录）

## 访问日志与追踪

- 访问日志：JSON 格式，包含 trace_id、request_id、client_ip、route、status、latency、upstream_service、policy_flags。
- 日志去敏：遵循 PII 列表掩码规则，在 DSL/global 中配置需要脱敏的字段。
- Trace：OpenTelemetry，采样 & tag（route、service_version）需可配置。

## 故障处理

1. 下游错误率暴增
   - 检查熔断器状态与历史变化
   - 暂时降低 downstream 权重或切换备用后端
   - 检查下游服务日志与可用性

2. 配置下发后出现大量错误
   - 立即触发回滚（控制平面）
   - 回滚后分析 diff 并本地 reproduce

3. 高并发导致资源耗尽（高 CPU/队列溢出）
   - 启用临时全局限流或 reject 非关键路由
   - 扩容实例（HPA / 手动）

4. 访问日志吞吐瓶颈
   - 降低日志 verbosity 或增加日志采样
   - 使用异步批量发送并开启背压策略

## 性能测试与压力测试

- 工具：wrk、k6、locust、JMeter
- 测试要点：QPS、并发连接数、P95/P99 延迟、队列长度、重试与重试后端行为
- 长尾测试：关注 P99/P999 以及系统在高并发下的稳定性与降级策略

## Chaos 测试（故障注入）

- 场景：网络延迟、丢包、下游 5xx、大规模实例重启
- 验证点：熔断生效、降级返回、自动回滚、告警触发、无数据丢失的审计

## 审计与合规

- 配置变更审计：记录变更者、变更内容、时间戳、审批记录
- 访问日志保留策略：配置化（例如 30 天/90 天），并对敏感字段做脱敏
- 密钥管理：使用 Vault 管理私钥与 secret，控制平面负责 secrets 分发

## 发布与灰度流程

1. 本地验证 DSL 与静态检查
2. CI 执行 lint + schema 校验
3. 提交至控制平面并发起审批（如需）
4. Canary 下发（5% 实例）并监控 30-60 分钟
5. 若通过，全量下发并继续监控
6. 若异常，执行回滚并发起 RCA

## 常见问题与 FAQ

Q: 如何快速禁用某条路由？
A: 在 DSL 中将 route 状态置为 disabled 并下发，或通过控制台临时添加 deny filter。

Q: 如何降低日志量？
A: 调低 global.logging.level 或在特定 route 上配置更严格的日志采样。

Q: 如何处理不可重试的请求误触发重试？
A: 在 route 上明确配置 retry 策略，并仅对幂等方法启用重试。

## 演进建议

- 将网关与 Service Mesh 对接以统一观测与安全策略
- 引入 ML 驱动流量预测与自动调节
- 扩展 DSL 表达能力（更复杂条件、时间窗、地理规则）
