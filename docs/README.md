# API/AI Gateway 设计文档

## 项目简介

基于 Go 实现的高性能、可观测、可治理的 API/AI Gateway，用于微服务与 AI 推理服务的统一接入、治理与流量控制。

## 目标与范围

- 统一接入：支持 HTTP/gRPC 等协议，提供统一入口、路由与转发。
- 微服务治理：鉴权/授权、参数校验、限流、熔断、灰度、重试等。
- 可观测性：Prometheus 指标、OpenTelemetry tracing、结构化访问日志。
- 高可用：控制/数据平面分离、集群化部署、配置热加载与回滚。
- 高性能：异步化处理、连接池、下游复用、批量/排队策略。
- 配置 DSL：声明式配置 DSL，支持版本化与热下发。

## 总体架构

- 控制平面（Gag Admin）：管理 DSL 配置、策略、证书、审计、下发配置到数据平面；提供 UI/API 与审计流程。
- 数据平面（Gag Server）：无状态/轻状态，高性能请求路径，执行 filter chain（pre → route → invoke → post），上报 metrics/logs/traces。

数据平面实例通过安全通道（mTLS/gRPC）与控制平面通信，支持增量、流式的配置下发与确认回执，实现热加载。

## 详细设计

## 参考文档

- [DSL设计](../pkg/mk/README.md)
- [运维手册](./OPERATIONS.md)
