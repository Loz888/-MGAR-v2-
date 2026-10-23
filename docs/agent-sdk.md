# P2 Agent SDK

Version: agent-sdk.v1

P2 在 P1 持久控制面之外提供 Python 决策和角色工具适配层。SDK 不持有控制面管理员凭据，
不直接更新 Task、Workflow、计划、预算账本或独立验证结论。
`Agent.run(task, snapshot, tools, budget)` 的最终输出是未验证候选结果；
受信调用者须继续使用 Go 的 Claim/Epoch/Revision/Lease 和版本守卫才能提交 output_ready。
SDK 局部回合与工具额度不代替 PostgreSQL 的全局成本预留，SDK 事件也不代替数据库审计。

模型只能提出严格 JSON 的工具调用或最终结构化输出。工具注册、角色、参数 Schema、
资源域、最大输出与超时由受信配置决定；目标和检索文本不能扩展能力。
ContextSnapshot 遵循既有 context.v1 契约，并与任务身份和计划版本匹配。
配置、上下文、Observation 和事件保留固定副本，后续轮次不能改写已发生的历史。

默认完全离线；Mock 只替代模型决策，工具本身实际执行。Mock 结果不能证明真实模型推理能力。
SDK 的结构完成不能宣称证据独立验证成功。坏 JSON、未授权工具、安全失败或预算耗尽都必须有界结束。

本地工具处理器只允许受信实现，不能把任意代码注册成处理器并据此声称沙箱隔离。
Linux/WSL2 的主线程期限防护与各适配器的 HTTP、SQL、子进程超时共同限制执行。
不能用遗留后台线程冒充停止执行；完整不可信代码隔离由 P5 交付。
跨进程恢复和计费结算仍由 Go 持久控制面及后续对应任务负责。

P2-01 证据：`evidence/batches/P2-01/accept.json`。各后续角色和 Provider 仅在对应任务验收后列为已实现。
