# API/AI Gateway 设计文档（概览）

本文件为本仓库 gag 的设计简要说明，包含网关目标、架构概览与阅读指南。详细 DSL 规范与运维手册见 docs/DSL_SPEC.md 与 docs/OPERATIONS.md。

## 项目简介

本项目目标是实现一个高性能、可观测、可治理的 API/AI Gateway，用于微服务与 AI 推理服务的统一接入、治理与流量控制。重点关注：链路追踪、实时监控、访问日志、参数校验、鉴权、限流、熔断降级、灰度分流等能力。

受众：网关开发者、平台 SRE、运维与使用方（后端服务 owner）。

## 目标与范围

- 统一接入：支持 HTTP/gRPC 等协议，提供统一入口、路由与转发。
- 微服务治理：鉴权/授权、参数校验、限流、熔断、灰度、重试等。
- 可观测性：Prometheus 指标、OpenTelemetry tracing、结构化访问日志。
- 高可用：控制/数据平面分离、集群化部署、配置热加载与回滚。
- 高性能：异步化处理、连接池、下游复用、批量/排队策略。
- 配置 DSL：声明式配置 DSL（已在 pkg/mk 目录实现），支持版本化与热下发。

## 总体架构（高层）

- 控制平面（Control Plane）：管理 DSL 配置、策略、证书、审计、下发配置到数据平面；提供 UI/API 与审计流程。
- 数据平面（Gateway 实例）：无状态/轻状态，高性能请求路径，执行 filter chain（pre → route → invoke → post），上报 metrics/logs/traces。

数据平面实例通过安全通道（mTLS/gRPC）与控制平面通信，支持增量、流式的配置下发与确认回执，实现热加载。

## 快速阅读指引

- 阅读本文件获取整体理解。
- 阅读 docs/DSL_SPEC.md 获取 DSL 语法、实体与示例配置。
- 阅读 docs/OPERATIONS.md 获取运维与 SRE Runbook、监控告警、部署与回滚步骤。

---

（文档位于 docs/ 目录：README.md / DSL_SPEC.md / OPERATIONS.md）
