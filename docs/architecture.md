# MGAR v2 架构边界

Version: architecture.v1
Implementation: DESIGN_ONLY
Boundary: SINGLE_HOST_LINUX_WSL2

本文保留 P0-01 的设计冻结快照；下文“当前未实现”指该批次的验收范围。
P1 控制面实现与剩余边界见 [阶段限制](limitations.md)、[开发入口](development.md)；
生命周期、恢复与终态决策补充见 [ADR-0002](adr/ADR-0002.md)，以各批次真实证据判定实现状态。

本卡只冻结设计与离线静态检查。Go 控制面、Python Agent、数据库、沙箱和模型能力均**当前未实现**。
依据：[项目宪章](../01_PROJECT_CHARTER.md)、[架构契约](../02_ARCHITECTURE_CONTRACTS.md)、
[工作协议](../03_CODEX_WORK_PROTOCOL.md)、[实现决策](../07_IMPLEMENTATION_PLAYBOOK.md)。
后续实现不得把文档检查 PASS 当作运行时验收；协议改变须先提交 ADR 并取得对应任务授权。

## 1. 组件与信任边界

```text
CLI/HTTP → Go Control Plane → PostgreSQL + Event/Audit Ledger
                    ↓                 ↕ Private Artifact CAS
              Python Worker → ToolGateway → Reader / Sandbox
                    ↓
            Independent Verifier → Aggregator
```

Go 负责确定性状态转移、计划 CAS、就绪判定、执行授权与预算。
Python 负责受控 Agent Loop、Provider 和工具适配，通过受控 API 提交结果；不取得任意控制表 SQL 权限。
LLM、检索文档和工具输出都是数据，不能授予自身权限或直接更新权威状态。

| 主体 | 信任与授权边界 |
|---|---|
| GoControlPlane | 可信状态写入者；服务端身份、Capability、参数、资源域与预算检查后才进入短事务。 |
| PythonWorker | 执行进程；每个 Task 有独立身份、上下文与权限，经受控 API 写入；不得直接改他人任务。 |
| LLM | 只提议计划或工具请求；候选内容经 Schema、图、权限与预算校验，不能自行授权。 |
| DBReader | 独立只读数据库账号、READ ONLY、超时、行限制和表白名单；只接合成业务库。 |
| Sandbox | 不可信代码；无控制面/模型密钥、非 root、禁网、只读根目录、最小挂载、资源上限。 |
| Verifier | 与受审 Agent 权限分离；重执行并比对证据；Reviewer LLM 不能覆盖确定性失败。 |
| Aggregator | 只汇总有权限、来源和验证状态的 Artifact；未知或未验证项明确列出。 |
| EventLedger | 数据库事件是审计权威；Trace 只是排障视图，可能丢失，不能替代持久状态。 |

## 2. 单机边界与能力声明

目标部署范围是单机 Linux/WSL2；未来需真实跨两个 OS 进程和 PostgreSQL 事务测试。
不承诺跨主机容错；不承诺断电恢复语义；不承诺外部 exactly-once。
内部 Handoff 设计为至少一次投递与幂等消费，不能推导出外部副作用恰好一次。
DB 与文件系统没有共同原子事务；内容先落盘可能产生 orphan，须可审计 inventory 和受控 GC。

MockProvider 是完全离线的确定性测试替身，Mock 不等于真实 Agent。
Mock 工程正确性与真实模型任务正确率分别统计；本卡未执行任何模型实验。
付费调用默认关闭，未来只有 MGAR_ALLOW_PAID=1、全实验 MGAR_MAX_COST_USD、Provider credential
和显式 case 白名单同时满足时才允许；生产业务数据库写入不在项目范围。

默认执行边界和旧 Runtime 关系见 [ADR-0001](adr/ADR-0001.md)。
运行时状态机与受控写入要求见 [协议索引](protocols.md)。

## 3. 术语

| 术语 | 定义 |
|---|---|
| WorkflowRun | 一次用户目标的持久化生命周期，由 run_id 标识；终态不可逆。 |
| PlanVersion | 同一 Run 的追加式计划版本，经 expected_plan_version CAS 激活。 |
| NodeKey | 当前计划中的稳定逻辑节点名；跨版本通过 plan_nodes 映射任务实例。 |
| TaskInstance | 一次有独立 task_id 的执行实例；重规划创建新实例或显式合法复用，不重写历史终态。 |
| Worker | OS 执行进程，不等于 Agent；可承载多个具有独立上下文和权限的任务实例。 |
| AgentRole | Planner、ResearchAgent、DataAgent、ActionAgent、Verifier、Aggregator 等职责；角色不靠 Prompt 强制。 |
| Epoch | 接管时递增的 fencing token；旧执行者无法通过受控写入检查。 |
| Lease | 使用数据库时间判断的限时执行授权；失效后停止受控写入。 |
| Revision | 实体受控更新的乐观并发版本；旧 expected_revision 被拒绝。 |
| Artifact | 不可变内容及来源元数据；内容寻址不代替身份和授权。 |
| ContextSnapshot | 固定 Artifact 版本、权限域及 Prompt/Tool/Model 配置的输入快照。 |
| Evidence | 可重执行、有来源和预期观察的结构化事实声明，不以模型自述作为证明。 |
| Verdict | 独立验证结果；在 artifact_verdicts 中追加，verification_status 只作派生缓存。 |
| LogicalCall | 一个逻辑模型步骤的 logical_call_id，不等于一次物理请求。 |
| PhysicalCall | 一次提供商请求的 call_id；未知结果计保守费用上界，不默认重播。 |
| MockProvider | 离线测试替身，不能证明真实模型能力或准确率。 |
| ExecutionAdapter | 可替换的受控执行边界；默认本地适配器，旧 Runtime 集成为可选项。 |

## 4. 持久化实体目录

以下逐项转录 02.B 的必备字段/索引；不是 DDL，不新增持久化协议。正式迁移属于 P1-01。
枚举由 CHECK/受控表约束，时间戳用 TIMESTAMPTZ，迁移追加式；禁止用 AutoMigrate 代替迁移。

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

`artifacts.verification_status` 只能是派生视图，权威验证历史在 artifact_verdicts。
历史 task_instances 终态保持不变，stale 仅描述节点在当前计划下的有效性。

## 5. 冻结的十二项不变量

下列规范文本与 01.6 一致；负责组件与后续验证映射只表示设计责任，均未实现。

| 编号 | 冻结规范 | 责任 / 后续验证 |
|---|---|---|
| I1 | 非终态工作流才能新增任务；终态不重新进入 running。 | Run 生命周期 / P1-05（未实现） |
| I2 | 每个激活计划 DAG 有界且无环；节点依赖只引用同一工作流中的有效节点。 | Plan Validator / P1-02（未实现） |
| I3 | 计划更新追加新 Plan Version，通过 `expected_plan_version` CAS 激活；旧计划及已产生证据不得篡改。 | PlanStore / P1-02（未实现） |
| I4 | 只有全部依赖在当前计划语义下满足且所需 Artifact 版本有效，任务才可进入 ready。 | Scheduler / P1-03（未实现） |
| I5 | 每个任务实例的提交必须同时检查身份/epoch/lease/revision；过期 Owner 永远不能提交结果。 | Claim/Fencing / P1-04（未实现） |
| I6 | Handoff 事件与状态更改同一事务持久化；至少一次投递 + 幂等消费，不承诺外部 exactly-once。 | Handoff / P4-01（未实现） |
| I7 | Artifact 不可原地改写；每个消费任务固定 `artifact_id + content_hash + plan_version` 输入快照。 | Artifact/Context / P3-01、P3-02（未实现） |
| I8 | 未验证/无授权产物不能被标记为已验证成功；Verifier 与受审 Agent 权限分离。 | Verifier/Policy / P5-01、P5-03（未实现） |
| I9 | 全局成本/次数/并发预算在 DB 或可信控制面原子预留；未知付费调用按保守上界记录。 | Budget / P6-01（未实现） |
| I10 | 沙箱里的不可信代码不持有控制面/模型 API 密钥；危险工具默认拒绝。 | Sandbox / P5-04（未实现） |
| I11 | 所有产物、Trace、模型用量与结果均可回链到 Workflow/Task/Artifact/Call，敏感内容默认不进日志。 | Observability / P6-04（未实现） |
| I12 | Mock 工程正确性与真实模型任务正确率，分别统计和表述。 | Evaluation / P7-03、P7-06（未实现） |

## 6. 本卡可复现检查

在仓库根目录运行：

```bash
python scripts/validate_plan_docs.py
python tests/test_plan_docs.py
git diff --check
```

仅需 Python 标准库，无网络、数据库、模型或 Docker 依赖。检查文档版本、必备实体/字段、
术语、十二项不变量、状态表、接口和 Schema 索引、链接及已知越界表述。
负例测试只在临时目录修改文档，验证遗漏、漂移与越界声明能导致真实非零退出码。
静态表述检查是有限规则，不是自然语言语义证明，仍需人工评审。
本卡证据在 `evidence/batches/P0-01/accept.json`，不代表任何运行时、数据库或多 Agent 验收。
