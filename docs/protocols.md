# MGAR v2 状态与接口目录

Version: protocols.v1
Implementation: DESIGN_ONLY

来源：[02 架构契约](../02_ARCHITECTURE_CONTRACTS.md)、[07 实现决策](../07_IMPLEMENTATION_PLAYBOOK.md)。
这里冻结协议目录与约束，所有运行时接口当前未实现；不引入 DDL、HTTP 路径或新状态枚举。
Schema 样本见 [契约索引](../contracts/README.md)，锁顺序见 [ADR-0001](adr/ADR-0001.md)。

## 1. Workflow 状态转移表

| 当前状态 | 后继状态 | 门禁 / 责任 |
|---|---|---|
| queued | planning | 有效编排器租约；Go 控制面。 |
| planning | running | 有界无环计划经校验和 CAS 激活。 |
| running | verifying | 当前计划必要节点满足，进入独立验证。 |
| verifying | succeeded | 独立证据与必要节点满足，终态事件持久化。 |
| verifying | failed | 验证失败按策略收尾，保存失败证据。 |
| verifying | cancelled | 受控取消并审计；Worker 不得自行取消整个 Run。 |
| succeeded | ∅ | 终态不可迁出。 |
| failed | ∅ | 终态不可迁出。 |
| cancelled | ∅ | 终态不可迁出。 |

## 2. TaskInstance 状态转移表

| 当前状态 | 后继状态 | 门禁 / 责任 |
|---|---|---|
| pending | ready | 当前 Plan 全部直接依赖 AND join，Artifact 版本/授权/验证满足，预算允许。 |
| ready | claimed | 身份授权、attempts 上限与预算原子预留，分配 owner/epoch/lease。 |
| claimed | executing | 有效授权的 Worker 开始执行。 |
| executing | output_ready | 产物引用、任务状态、event/outbox 同事务持久化。 |
| output_ready | verifying | Go 控制面委派独立 Verifier，不能用模型自称通过。 |
| verifying | succeeded | 有效 verdict 与受控结果提交。 |
| verifying | failed | 独立验证失败留痕。 |
| pending,ready,claimed,executing,output_ready,verifying,blocked | cancelled | 02.C 允许从非终态受控取消；必须校验授权和竞争条件。 |
| succeeded | ∅ | 保留历史终态；不得改写为 stale。 |
| failed | ∅ | 保留历史终态。 |
| cancelled | ∅ | 保留历史终态。 |

## 3. 异常状态与恢复

| 状态 | 进入/退出边 | 规范已有语义 |
|---|---|---|
| blocked | 未指定 | Workflow 审批/预算等待；Task 依赖/审批等待，Worker 无权解锁。 |
| interrupted | 未指定 | Workflow 恢复条件不足时安全终止或等待处理；不是新增终态。 |

前两表转录 02.C 的主路径与明确取消规则，**不是完整运行时授权表**。
02.C 未穷举异常进入/恢复、提前失败或 Workflow 提前取消的每条边；不猜测补齐，未规定路径默认拒绝。
后续卡实现这些路径前须先受审 ADR 与相应正负例；不得通过文档静态检查暗中授权新写路径。

过期 claimed/executing 在授权下以 epoch+1 接管，恢复前读取步骤、model_calls 和 tool_invocations。
接管只更新执行授权，不能据此盲重放非幂等操作。
unknown 模型请求不默认重播，按费用保守上界记录并保留人工处置路径。
output_ready 是已持久化产物；已有验证结果在恢复时复用，不重复计费或假造成功。

## 4. 受控写与事务责任

每次受控写检查 owner、epoch、lease_until > db_now、expected_revision、legal_transition、
current_plan_generation_if_relevant；成功更新 revision，拒绝须区分 fenced、CAS、lease expired、bad state。
LLM 和 Worker 不能直接更新任意权威状态。续租失败停止当前操作和受控写，不取消整个 Run。
数据库时间决定租约有效性；时钟回拨、超时、断电和跨主机恢复语义尚未实测，不能推断保证。

| 事务 | 必须原子持久化 | 事务外动作 / 守卫 |
|---|---|---|
| PlanCommit | plans + plan_nodes + plan_edges + active version CAS + revision + outbox/event | DAG 预校验在外，事务内重验编排器 lease、base_version；网络在提交后。 |
| TaskResultCommit | result_artifact_ref + output_ready + event/outbox | owner/epoch/lease/revision 全校验；CAS 文件先落盘可能留下 orphan。 |
| ClaimAndReserve | 预算预留 + owner/epoch/lease/revision + 执行授权 | 先 Workflow 后 Budget 后 Task；SKIP LOCKED 不保护预算。 |
| HandoffConsume | inbox processed + 下游创建/解锁 | received 不等于 processed；(consumer_name,event_id) 幂等。 |
| ModelCallReserve | model_calls reserved + 全局预算预留 | 提交后才能网络调用；unknown 不作零费用、不默认重播。 |

统一锁顺序按 ADR-0001；所有 LLM、远程 API、工具和测试执行在数据库短事务之外。
计划门禁：MaxNodes=24、MaxDepth=6、MaxFanout=8；同一 Run 有效节点、无环、合法角色、工具域、预算上界。
expected_plan_version / base_version CAS 激活追加版本；旧 Plan、Artifact 和历史任务不被覆盖。
新计划复用须匹配输入、Artifact Hash、工具/策略版本和权限；旧计划完成不能自动算作新计划成功。
Handoff 至少一次投递 + 幂等消费；不承诺外部 exactly-once。

## 5. 对外接口索引

全部是 02.L 的设计态 CLI；本卡未创建可执行程序。

| CLI | 职责 |
|---|---|
| `mgarctl submit --fixture incident --provider mock` | 提交合成任务，默认离线 Provider。 |
| `mgarctl status <run-id>` | 查询权威 Run 状态。 |
| `mgarctl trace <run-id> --format json` | 关联可观测性与审计，Trace 不替代 DB 事件。 |
| `mgarctl cancel <run-id>` | 服务端授权取消。 |
| `mgarctl approve <approval-id> --action-digest <sha>` | 显式一次性审批，绑定 exact scope、expiry、task_id；不自动执行。 |
| `mgarctl resume <run-id>` | 检查恢复条件，不重开终态。 |
| `mgarctl inspect-artifact <artifact-id>` | 经 ACL/hash 检查读取产物。 |
| `mgarctl eval --suite offline --report-dir evidence/eval/<id>` | 冻结数据集的离线评测，不能冒充真实模型实验。 |

## 6. 内部接口目录

| 接口 | 责任 |
|---|---|
| `PlanStore.ProposeAndCommit(ctx, runID, baseVersion, proposal)` | 有界 DAG 校验与原子计划激活。 |
| `Scheduler.GetReady(ctx, runID)` | 全依赖、Artifact、权限与预算条件判定。 |
| `Handoff.PublishTx(...)` | 状态和 durable 通知在同一事务提交。 |
| `ContextBuilder.BuildSnapshot(...)` | 固定输入、来源、权限域与版本；summary 是有损未验证内容。 |
| `Budget.ReserveModelCall(...)` | 全局原子预留、结算与未知费用上界。 |
| `Verifier.Check(...)` | Schema、来源、环境、重执行与事实比对。 |
| `Agent.run(task, snapshot, tools, budget) -> AgentResult` | Python 受预算控制的 Agent Loop；不开放任意数据库 SQL。 |
| `ExecutionAdapter` | ADR-0001 的可替换执行边界；本卡不新增方法签名。 |

实体字段与十二项不变量见 [架构文档](architecture.md)。接口名是设计目录，不是实现可用性承诺。
