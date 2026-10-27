# 02 · 架构与协议契约

## A. Go / Python 分层

```text
CLI / HTTP（用户提交、查询、取消、审批）
    ↓
Go Control Plane
  PlanValidator / PlanStore / DAG Scheduler / Dependency Tracker
  Handoff Outbox-Inbox / Context Selector / Cost Router / Policy Gateway
    ↕ 受控数据库事务
PostgreSQL + Private Artifact CAS + Event/Audit Ledger
    ↓ 任务授权 / 受控协议
Python Worker SDK（独立进程）
  ResearchAgent / DataAgent / ActionAgent / PlannerAdapter
    ↓ 服务端 Gate 的 Tool Invocation
Readonly Tools / DB Reader / Sandboxed Go Tests
    ↓
Independent Verifier + Aggregator
```

至少支持跨两个独立 OS 进程并行执行。控制面可单机部署，明确不声称跨主机容错；所有并发正确性靠 DB 真事务验证，而非仅 goroutine 或 Mock Store。

## B. 最小持久化实体

> 数据库迁移全部追加式，ORM 自动 `AutoMigrate` 不得代替显式迁移。所有主键使用 UUID 或数据库生成 ID 并统一接口；时间戳 `TIMESTAMPTZ`。所有枚举使用 `CHECK` 或受控枚举表。

| 实体 | 必备字段/索引 | 一致性职责 |
|---|---|---|
| `workflow_runs` | `run_id, goal, state, active_plan_version, revision, created_at, finished_at` | 整体任务生命周期；终态不可逆 |
| `orchestrator_leases` | `run_id PK, owner_id, epoch, lease_until, revision` | 仅持有有效 Orchestrator Claim 的实例能够推进计划/收尾 |
| `plans` | `(run_id, plan_version) PK, plan_hash, proposer, schema_version, created_at` | append-only；旧版本可审计 |
| `plan_nodes` | `(run_id,plan_version,node_key) PK, task_instance_id, role, requirements, reuse_artifact_id` | 每个计划版本上的节点映射，不能覆盖旧计划 |
| `plan_edges` | `(run_id,plan_version,from_key,to_key) PK` | DAG 依赖；提交时校验无环 |
| `task_instances` | `task_id PK, run_id, node_key, role, state, owner, epoch, lease_until, revision, retry_count, attempts, result_artifact_id` | 一次物理任务执行，Epoch / CAS 守卫 |
| `task_inputs` | `(task_id,artifact_id)`, `content_sha256, producer_node, source_plan_version` | 锁定消费版本，避免 TOCTOU |
| `artifacts` | `artifact_id, kind, content_sha256, storage_uri, schema_version, producer_task, created_at, verification_status` | 不可变 CAS 元数据；不可将验证状态当成原始内容覆盖 |
| `artifact_verdicts` | `verdict_id, artifact_id, checker_version, passed, evidence_artifact_id` | append-only 验证历史 |
| `outbox_events` | `event_id, aggregate_type, aggregate_id, event_kind, payload, created_at, published_at` | 状态提交同时产出 durable 通知 |
| `inbox_receipts` | `(consumer_name,event_id) PK, first_seen_at, processing_status` | 消费去重；幂等重新进入 |
| `model_calls` | `call_id, logical_call_id, run_id, task_id, provider, model, status, usage, estimate_bound, reservation, settlement, created_at` | 真实调用账本；未知结果不重播 |
| `tool_invocations` | `invocation_id, task_id, capability, scope, request_hash, status, result_artifact_id` | 工具审计、读写副作用分类 |
| `run_budgets` | `run_id PK, max_model_calls, max_tokens_est, max_cost_usd, max_delegations, used/reserved/settled counters` | 单 Run 原子预留；不足即拒绝 |
| `approval_requests` | `approval_id, run_id, task_id, action_digest, requested_scope, state, approver, expiry` | 一次性审批绑定具体参数摘要 |
| `workflow_events` | `event_id PK, run_id, entity_type, entity_id, entity_revision, kind, trace_id, payload` | 审计；幂等键 `(entity_type,entity_id,entity_revision,kind)` |

**Artifact 纠正：** 内容与元数据不可变；检验记录存在 `artifact_verdicts` 追加表，`artifacts` 中若包含 `verification_status` 只能作为缓存派生视图，不能把它当成唯一权威状态。

**Plan 纠正：** `task_instances` 的已完成记录不能通过更新为 `stale` 篡改历史。新 Plan 通过 `plan_nodes` 引用新任务实例或合法复用旧 Artifact；`stale` 属于节点在**当前计划**下的有效性，不是修改历史任务终态。

## C. 工作流 / 任务状态机

### Workflow
`queued → planning → running → verifying → succeeded|failed|cancelled`，另有 `blocked`（需用户审批/预算）和 `interrupted`（因恢复条件不足安全终止或等待处理）。终态 `succeeded|failed|cancelled` 不再迁出；审计留痕。

### Task Instance
`pending → ready → claimed → executing → output_ready → verifying → succeeded|failed`；从非终态可转 `cancelled`。`blocked` 是依赖或审批状态，不可被 Worker 随意宣告可运行。

Worker 死亡后，过期 `claimed|executing` 可在授权下以 **epoch+1** 接管；恢复前要读取持久化的步骤状态、模型账本和工具调用账本。不可重复执行已知或未知的非幂等外部操作。任务的 `output_ready` 是持久化产物，Verifier 的成功属于独立 verdict，不得和模型声明混淆。

## D. Plan Commit 与 Dynamic Replanning

`ProposePlan(run_id, base_version, nodes, edges, intent)` → 严格 Schema → `CheckMaxNodes≤24 / MaxDepth≤6 / MaxFanout≤8 / NoCycles / DependencyExists / AuthorizedRoles / ToolScopes / BudgetUpperBound` → 在一段短事务中：

1. 锁住 `workflow_runs`（必须先验证 Orchestrator 所有权 lease）；确保 `active_plan_version == base_version`。
2. 为新版本写入 append-only `plans, plan_nodes, plan_edges`；对每个节点计算所消费上游 Artifact 的版本与摘要，合法复用旧结果只在依赖/策略/工具版本哈希完全一致时允许。
3. CAS 更新 `workflow_runs.active_plan_version = base_version+1` 且 `revision+1`。
4. 产生 `plan_committed` Outbox Event；事务提交后异步通知。

重规划必须显式给出理由（新增证据、失败类型、用户改变目标等）；不得悄悄移除失败证据；上一个 Plan 的任务若正在运行：允许完成历史实例但**不得**在新 Plan 上自动算成功，输出需重新绑定或丢弃到旧版本；必要时发取消信号，保持 Fencing。

## E. DAG 就绪、Fan-out / Fan-in

- `ready(n)` ⇔ n 所有直接前驱在当前 Plan 完成，所需 Artifact 均符合来源、验证与权限，且预算允许调度。
- 依赖以稳定 `node_key` 表示；对**全部**前驱执行 AND join。成功事件重复投递仍只触发一次 Task Instance。
- 若上游永久失败，下游由策略进入 `blocked/failed` 或触发有预算的 `replan`，不得卡死在 pending 无出口。
- Scheduler 只优化**已就绪**任务，不得为了关键路径抢跑未满足依赖的节点。
- 计划完成判定由当前有效 Plan 的必要节点、独立 Verifier 结果和终态事件共同决定；不以“所有 Worker 退出”作为成功判定。

## F. Claim / Fencing / Recovery

- Claim：短事务中按一致顺序锁相关预算/Workflow/Task 行，验证任务在当前 Plan 中可执行，`attempts < max_attempts`，再原子分配 owner、epoch、lease；不能只先 SELECT 再无条件 UPDATE 预算。
- `SKIP LOCKED` 仅在队列任务行争用上使用；它不自动保护 Run Budget、Plan Version 或审批行。
- 受控写：校验 `(owner, epoch, lease_until > db_now, expected_revision, legal_transition, current_plan_generation_if_relevant)`，成功更新 revision；拒绝时区分 fenced、CAS、lease expired、bad state。
- 续租失败 = 该 Worker 取消当前操作并停止受控写；禁止自行取消整个 Workflow。
- 原则：DB 使用数据库时间判断租约，所有超时与时钟回拨边界写进 `docs/limitations.md`；没有多物理主机/断电故障实测不做相关承诺。
- **锁顺序**：明确在 ADR 中冻结顺序，例如 `workflow_runs → run_budgets → task_instances → outbox`，所有写路径遵循；单独测试相反调用场景没有死锁。若索引 Claim 首先锁 Task，则预算原子条件更新必须另行严格证明，并做并发测试。

## G. Durable Handoff（Outbox/Inbox）

`Agent A commit Result Artifact + task state + outbox(task_succeeded)` 同事务；dispatcher 至少一次发送通知；`Agent B` 以 `(consumer,event_id)` inbox 幂等消费，原子创建/解锁 ready 工作。

关键断言：消息丢失可扫描 outbox 补投；消息重复不重复建任务；接收者崩溃可恢复；乱序消息按 Plan Version、实体 Revision 检查；旧 Agent 不得绕过 Orchestrator 修改别人任务。

Outbox 发布标记不等于业务处理成功；Inbox receipt 须定义 `received/processed`，只有处理结果与 receipt 在同事务提交才算已消费。

## H. Artifact / Context 与安全

- Artifact URI 为 `artifact://<id>`，内容寻址摘要 SHA-256 + 字节大小 + 内容类型 + Producer + Schema Version。磁盘写入 `temp → fsync → atomic rename`（同文件系统），读时 hash 验证；DB 关联写与磁盘写之间可能崩溃，需 orphan inventory / GC 安全协议，不得宣称跨 DB-FS 原子事务。
- `ContextSnapshot` 是固定的 Artifact 引用清单 + 权限域 + Prompt/Tool/Model 配置版本；同一 Task 重放可以重建相同输入，不靠当前共享内存。
- Context Selector 必须基于任务目标、上游依赖、角色 ACL、Token/字节限制进行选择，返回每条来源/纳入原因/拒绝原因；压缩摘要额外标记 `summary`, 永不当原始事实使用。
- 外部检索内容不得直接改变系统指令；任何工具调用都经 `ToolGateway` 的身份、Capability、参数、资源域校验。

## I. 模型与预算

- 每个物理调用有 `call_id`，逻辑步骤有独立 `logical_call_id`；**不能简单把 prompt SHA 作为全局幂等键**（不同任务可能合法使用相同 Prompt）。
- 前置准入事务：在 `run_budgets` 行锁/原子条件更新下检查全局次数、成本、最大 Token 估计与并发额度并预留，同时持久化 `model_calls(status=reserved)`；**交易提交之后**才发送模型请求。
- 调用返回 `succeeded|failed` 保存提供商 usage/错误类型；远程请求已发但无可靠响应，进入 `unknown`，保留上界费用与可能副作用；不得把 unknown 记作 zero，也不得默认重新调用。
- 账本“估算上界”不等于真实账单；`usage_known=false` 时 cost 只能标为估算；预算结算交易保证 ledger/aggregate 守恒。
- paid 默认关闭；必须有 `MGAR_ALLOW_PAID=1` + `MGAR_MAX_COST_USD` + Provider credential + 明确实验 case 列表。费用上限针对全实验而不仅单 Run。

## J. Tool Capability / Sandbox

- 工具声明：`tool_name, schema_version, effect_class=readonly|idempotent_write|non_idempotent, scope, timeout, max_output, audit_policy`。
- DataAgent 只读 SQL：不同数据库账号 + `BEGIN READ ONLY` + statement_timeout + limit + 表白名单 + 经过维护的解析检查；不允许自动访问真实生产库。
- ActionAgent 在独立临时 Worktree 运行，文件 allowlist、大小/路径约束，最终不对宿主源目录直接写入。
- 不可信测试进程使用无凭据容器：非 root、只读根文件系统、最小挂载、禁网、CPU/内存/PID/时间上限、cap-drop、no-new-privileges；宿主 Worker 负责 DB 写和模型通信，绝不将凭据复制进执行容器。
- Approval 绑定 `action_digest + exact scope + expiry + task_id`，放行前再验证 token 和状态；任何审批超时安全拒绝。

## K. 可观测性

- 每个 Run 统一 `trace_id`，每个 Plan/Task/Agent/Tool/Model/Handoff/Verify 是 span 或事件；记录开始结束、结果、成本、来源、计划版本和摘要，不记录明文密钥/完整敏感 Prompt。
- 追踪可完整回答：谁提出计划、哪个 Agent 使用了哪个 Artifact 版本、哪些工具执行、哪个策略拒绝、哪个 Verifier 推翻结论、哪里发生接管。
- 数据库提交本身作为审计权威；Trace 可能因崩溃丢部分 Span，不能用 Trace 代替持久状态。

## L. 对外接口（稳定协议，非具体实现）

```text
mgarctl submit --fixture incident --provider mock
mgarctl status <run-id>
mgarctl trace <run-id> --format json
mgarctl cancel <run-id>
mgarctl approve <approval-id> --action-digest <sha>   # 本地显式审批；不自动执行
mgarctl resume <run-id>
mgarctl inspect-artifact <artifact-id>
mgarctl eval --suite offline --report-dir evidence/eval/<id>
```

内部 Go 接口建议：`PlanStore.ProposeAndCommit(ctx, runID, baseVersion, proposal)`, `Scheduler.GetReady(ctx, runID)`, `Handoff.PublishTx(...)`, `ContextBuilder.BuildSnapshot(...)`, `Budget.ReserveModelCall(...)`, `Verifier.Check(...)`。Python 接口建议：`Agent.run(task, snapshot, tools, budget) -> AgentResult`；共同使用版本化 JSON Schema，不开放原始任意数据库 SQL 给 Agent。
