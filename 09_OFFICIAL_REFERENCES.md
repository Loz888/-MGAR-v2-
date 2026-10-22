# 09 · 官方设计参考（用于对照，不替代本项目契约）

以下是编写任务书时参考的外部材料。Codex 实施前可再核实最新 API 和版本；不要将其视为已经运行的测试证据。

- [OpenAI Agents — Orchestration and handoffs](https://developers.openai.com/api/docs/guides/agents/orchestration)：多 Agent 中由 Manager 持续控制与独立 Handoff 的职责区别；本项目选择确定性 Go 控制面统一管理。
- [OpenAI Agents SDK — Agent orchestration](https://openai.github.io/openai-agents-python/multi_agent/)：LLM 规划与代码确定性编排可以组合；MGR 仅使用其思路，不要求依赖 SDK。
- [PostgreSQL SELECT / `SKIP LOCKED`](https://www.postgresql.org/docs/current/sql-select.html)：适合队列消费者减小锁争用，但不自动保护其他表的预算、Plan 或账户并发不变量。
- [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/)：Model/Tool/Agent span 属性；实现时冻结语义规范版本并保护敏感内容。

框架及标准会持续迭代，任务书避免绑定不稳定实现细节。注意项目的安全性来自独立的身份/权限/沙箱验收，而非引用协议名称本身。
