# Fan-in 合并与失败传播

Version: merge_result.v1

`Store.MergeContext` 只读取当前 aggregate Claim 的固定 ContextSnapshot，逐份通过 ReadInput
检查 Run、Role、版本、最新 verdict 与 CAS 哈希。结构化输入为 `facts.v1`：
`{"schema_version":"facts.v1","facts":[{"key":"paid_orders","value":12}]}`。
每份最多 64 个事实，值限非空标量类型（字符串可为空），拒绝重复 JSON 键、额外字段、重复事实名、
非有限数字及过大输入。通用 AgentResult 需要可信适配器先提取该格式，不能直接当成事实协议。

`handoff.Merge` 按稳定事实键和来源排序；同一事实值保留所有引用，不同值进入 conflict，
绝不按最后到达覆盖。每条引用单独标注 verified，可信事实优先排序，但未验证矛盾也不会消失。
精确 JSON 标量采用保守比较，例如 1 和 1.0 可产生冲突，需要调用方澄清，不自动猜测数值等价。

`AssessFanIn` 明确列出 required/optional 的缺失来源；必需来源未完成为 wait，永久失败为
replan（调用方有授权/预算时）或 fail。可选补充证据缺失可继续但必须留在 missing 中。
冻结 plan.v1 的所有 DAG 边仍是必需 AND 依赖；本卡不把其中任何一条变成可选，也不允许利用
可选标记绕过 Claim。`ResolveFanIn` 采用诚实失败策略，推进依赖 blocked 和 Workflow failed；
如需重规划，由受信 Orchestrator 在最终收尾前调用 P4-02 的有界接口。

`PublishMerge` 遇冲突/缺少必需证据，不发布报告；在 fenced 事务中将当前 aggregate 任务置 failed、
释放并发预留、写带快照 ID/结果摘要/冲突计数的审计和 Outbox。原始证据留在固定输入 CAS，可复查。
无冲突报告也只是 output_ready，仍需独立 verdict；`can_finalize` 是合并输入检查结果，不是 Workflow
成功授权。不能通过自己写 verified 字段获得可信身份。所有发布继续使用已有 Artifact 事务与租约校验。

本卡没有实现 P5 的独立 Verifier 或自动 Python Worker；测试用明确标注的合成 verdict。
