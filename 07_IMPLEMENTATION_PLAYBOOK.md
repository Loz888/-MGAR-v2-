# 07 · 可落地的实现决策、事务样例与冲突裁决

本章为实施方解决易错细节提供**规范性伪代码**。不等于可直接运行 SQL，P1 与后续应编写真实 migrations/queries 并做真 PG 测试。若要改变行为先提交 ADR + 相关测试。

## A. 五条无法妥协的事务边界

1. **PlanCommit**：validate(无环/上限/角色) 于事务外预验证；事务内仍锁 Workflow/Orchestrator Lease，再校验 base_version、提交 Plan + Edges + CAS 更新 active version + Outbox。失败时全部回滚；同时写事件。
2. **TaskResultCommit**：Task owner/epoch/revision/lease 正确才可提交 `result_artifact_ref + task output_ready + event/outbox`；若 Artifact CAS 先落物理文件，DB 回滚会遗留 orphan，后续只做**可审计** inventory 与受控 GC。
3. **ClaimAndReserve**：锁 Workflow 与 Budget（固定顺序），再选择 ready Task 锁行；检查预算后同一事务原子扣执行授权并推进 epoch/revision；不允许从两个并发 SELECT 都读到“还有最后一次”后各自扣一次。
4. **HandoffConsume**：Inbox 去重记录与创建/解锁下游 Task 同一事务提交。消费重试看到 processed 时直接返回已经处理的结果。
5. **ModelCallReserve**：模型网络请求发生在事务外；调用前持久化预留，返回后结算；网络结果不确定则标记 unknown，不自动重发。

## B. 错误分类与重试表

| ErrorKind | 例子 | 自动处理 | 保留证据 |
|---|---|---|---|
| `transient` | DB serialization failure、429/503（请求确知未被处理时） | 有次数和退避预算的重试 | 是 |
| `timeout_unknown` | 模型请求已发送但无响应 | **不得**盲重放外部收费调用，标 unknown | 是，按上界 |
| `stale_plan` | base version 过期 | 重新读取 DAG/输入再规划，不沿用旧输出 | 是 |
| `fenced` | 旧 epoch、lease 过期 | Worker 立刻停止写入，不取消整个 run | 是 |
| `evidence_rejected` | 验证错误数字 / 错误测试 | 有界 replan 或诚实 failed | 是 |
| `policy_denied` | 越权工具、非白名单 SQL | 默认不重试；如需要人工审批则 blocked | 是 |
| `invalid_plan` | 有环、非法 role、超出最大规模 | 拒绝；允许 Planner 有界修正，超过 budget 失败 | 是 |
| `permanent` | 无法访问必须资源 / 缺必要权限 | failed/blocked，不重复无效调用 | 是 |

每种 ErrorKind 独立单元测试+至少一个端到端负例。`retry_count` 只在**真正主动重试**时增加；Lease 接管、Plan 局部重规划、工具尝试、模型物理调用有不同预算。

## C. 计划版本与历史终态（务必注意）

```
Plan v1:  Research A@1 ─┐
                       ├─► Action C@1 (succeeded)
          Data B@1  ───┘

新数据出现：Data B@2
Plan v2:  Research A@1 (REUSE) ─┐
                                ├─► Action C@2 (NEW)
          Data B@2 (NEW) ───────┘
```

`Action C@1` 在历史计划中仍是 succeeded；`C@2` 是**新的 Task Instance**。不能把 `C@1` 的历史状态从 `succeeded` 更新为 `stale`。合法复用 `Research A@1` 必须对齐 Artifact Hash、工具版本/策略、访问权限、输入语义和需求约束。

## D. Context Selector 的性能实验要求

先实现：

- Full（对照）：当前任务有权访问的全部上游历史，按上限截断，记录截断的具体 Artifact。
- Summary（对照）：模型或规则的摘要+原始 provenance；摘要视为有损、未验证。
- Selected（实验）：基于直接依赖、实体匹配、任务角色和证据优先级选择 Artifact，记录分数/纳入与排除原因。实用先行，不要求一开始就上向量数据库。

禁止先把完整上下文传给模型、事后才统计“选择后 Token”。必须记录真实送给 Provider 的输入；上下文选取必须发生在模型调用之前。

## E. 调度器算法：从正确到优化

阶段 P1：`Ready Queue + strict dependency eligibility + first-ready`，先证明不变量。阶段 P6 才在 Ready 集合内加入 Critical Path Priority。

`critical_path_estimate(node) = local_estimated_cost(node) + max(child critical_path_estimate)`，用 DAG 逆拓扑计算；历史用时估计需定义冷启动默认值和统计来源。优化前后对照负载固定，报告公平性/饥饿保护开关。任何资源授权由单一可信账本判定，多个 Scheduler 不能分别有一套不相干本地计数器。

## F. 独立验证不是“第二个模型回答 Yes”

一条合格 `Evidence` 必须有：`producer_run/task, statement, evidence_kind, content_hash, input_snapshot_id, tool_version, execution_status, replay_recipe, expected_observation`。Verifier 至少执行：

1. **结构**：类型、必填、来源、访问范围、哈希和 schema_version；
2. **环境**：输入是否是固定的合成 DB 快照/源提交、工具版本/参数是否对应；
3. **重执行**：在可信控制面委派下运行只读 SQL 或无凭据沙箱 Go Test；
4. **结论比对**：比较结构化数值、退出码、预期业务断言；
5. **状态**：pass/fail/unverifiable，附证据与可解释失败原因。

Reviewer LLM 可以建议“再查一项”但不能覆盖 deterministic failure。

## G. 多 Agent 消融示意

固定一条复合任务，确保各组都能用到**相同的底层工具**；变动的是是否分角色、是否动态 DAG、是否筛选上下文、是否内部验证、是否允许局部重规划。

必须独立 Oracle 检查结果。系统内部 Verifier 是一项 treatment，不可同时充当唯一 Ground Truth；否则 Full 组天生因为“自证有效”而获得优势。

## H. 可观测性与预算对账

事件日志是权威源、Trace 是可视化与故障排查源。模型结果的 `usage_known` 与 `usage_source` 是必须字段；估计美元按冻结单价表计算且报告价格更新时间。`unknown` 用量按保守上界记录，同时将它和已知实收区分展示；不要令未知物理调用无限占用额度而无人工路径。

## I. 安全/权限测试的独立性

Role 不只靠 Python enum 或 prompt。测试用不同的真正数据库凭据/受控 API Token 模拟恶意 Worker，尝试跨 Run 读取、跨 Role 写入以及直接绕过 Tool Gateway 访问 DB。只要同一个凭据可更新任何 Task，便不能宣称角色是强信任边界，需要先修复授权模型。

## J. Codex 遇到需求冲突时遵循

- 不变量 > 安全边界 > 可复现性 > 功能丰富度 > 性能；
- 先最小可运行的正确实现、负例，再优化；
- 不直接把本章节伪 SQL 当成已测试的生产 SQL；
- 需求不充分就写 ADR，优先保守故障停机，不写“自动恢复成功”的假日志。
