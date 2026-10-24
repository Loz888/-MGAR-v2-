# Trace 与权威审计（P6-04）

Go SDK / OTLP HTTP exporter 固定 **1.38.0**，GenAI semantic conventions 固定 **1.37.0**（Schema URL 在代码中声明）。使用 `gen_ai.operation.name` 的 `invoke_agent`、`chat`、`execute_tool`，及已知输入/输出 token 属性；未知用量不填 0。任意模型名仅保留 SHA-256 摘要，Provider 当前只导出固定 `mock` 枚举。

`observability.ReadAudit` 使用 PostgreSQL repeatable-read 只读快照，关联 Workflow→Task→Agent→Model/Tool、收据 Handoff 与 Verify 事件，带计划版本、epoch、修订和账本成本。每类最多取 4097 项用于检测 4096 项上限，截断明确标记且禁止导出为完整投影。该 API 不读取 goal、原文 payload、工具参数、认证 Header 或 CAS 内容。Task/Agent 的 Span 是标注 `mgar.correlation_anchor` 的关联锚点，不代表 Agent 已执行；事件 Span 是数据库事件瞬时投影，账本 Span 是快照时点，不声称掌握模型内部耗时。

`ReadAudit.span_state=missing_or_not_collected` 表示尚无接收端观察证据；空审计为 `missing_empty_audit`。`Assess` 根据实际收到的 Span ID 返回 missing/partial/complete_projection/incomplete_audit，不能根据 Span 数量倒推业务成功。进程在 PG commit 后被 SIGKILL，仍可从已提交事件重建投影；未提交事件、丢失的实时 Span 和未记录细节不能恢复。缺少 outbox 时间或旧 trace_id 不匹配均显式显示，不改写历史。

`ExportLocal(ctx,audit,"http://127.0.0.1:4318")` 才会发送 OTLP protobuf，五秒总期限，禁用重试、代理、额外 Header、重定向与外部地址。SDK 会合并环境 Resource 属性，因此最终 `ReadOnlySpan.Resource()` 还做封闭白名单过滤，只保留固定 service.name。传输成功只意味着接收器应答，不保证后端永久保留；重复投影使用稳定 Span ID，去重能力取决于后端。

`deploy/otel-collector.yaml` 为本机 otelcol-contrib **0.137.0** 配置（loopback receiver、128MiB 内存限制、basic debug 输出）。本轮验证使用真实 SDK→本机 HTTP mock collector 的 OTLP 请求并解码 protobuf；没有下载/启动独立 collector 二进制，此项为可选 NOT_RUN，不把 YAML 当成已部署服务。

Python `TraceContext.for_agent` 与 Go 使用一致的 W3C traceparent 和哈希算法，只传播 traceparent；无 baggage / 认证信息，`safe_attributes` 接受闭集操作/角色和已知有界 token 数。Trace 上下文只作观测关联，任何任务读写仍依赖数据库身份、epoch 与租约。P7 才负责完整 Worker 自动接线。
