# MGAR v2 契约参考

Version: contracts-index.v1
Implementation: DESIGN_ONLY

这些 JSON 是 **设计态 Schema 样本**（不是运行代码）。P1/P2/P3/P5 的对应任务卡负责将它们复制/演进到新项目 `contracts/`，为 Python 和 Go 的入口/出口编写实际 validation 和负例测试。

约定：
- `schema_version` 每个协议必须显式保留；更新破坏兼容性时升主要版本。
- 模型产生的 JSON 必须同时过 Schema、图结构、角色权限、业务约束检查；JSON 合法不表示业务合法。
- `artifact_ref` 不允许用户自造本地路径，可信控制面负责映射 ID→真实对象、检查 ACL/哈希。
- 所有摘要要求 64 位十六进制 SHA-256，不承诺 SHA 代替版本号或执行所有权。
- 这里的 schema 只是可机器校验的基础；数据库事务及运行状态必须独立验证。

## 冻结索引（P0-01）

下表逐项列出当前样本的实际 required 列表，不代表已实现生产校验器；本卡不修改 Schema。
协议变化需先 ADR 与对应任务的正负例；示例不能替代 02/07 的身份、预算、图结构和信任约束。

| Schema | schema_version | required | 实现任务 |
|---|---|---|---|
| [plan.schema.json](plan.schema.json) | plan.v1 | schema_version, base_version, nodes, edges, rationale | P1-02（未实现） |
| [artifact.schema.json](artifact.schema.json) | artifact.v1 | schema_version, artifact_id, kind, content_sha256, byte_length, producer_task_id, source_plan_version, storage_uri | P3-01（未实现） |
| [context_snapshot.schema.json](context_snapshot.schema.json) | context.v1 | schema_version, task_id, plan_version, model_config_version, tool_policy_version, artifacts | P3-02（未实现） |
| [evidence.schema.json](evidence.schema.json) | evidence.v1 | schema_version, claim, kind, artifact_id, content_sha256, input_snapshot_id, tool_version, execution_status, producer_run_id, producer_task_id, source_plan_version, replay_recipe, observed, expected_observation | P5-01（实际校验已实现） |
| [verdict.schema.json](verdict.schema.json) | verdict.v1 | schema_version, artifact_id, status, checks, verifier_version | P5-01（未实现） |

实体和术语见 [架构](../docs/architecture.md)，CLI/内部接口与状态转移见 [协议目录](../docs/protocols.md)，
适配器和锁顺序见 [ADR-0001](../docs/adr/ADR-0001.md)。
这些 Schema 没有穷举运行时契约；例如 evidence 样本的结构合法本身不能证明来源、重执行和预期观察满足 07.F。
后续任务必须补足相应语义校验和测试，不可把静态索引检查当作运行时验证通过。

## P2 运行时补充

P0-01 的五项表格保持设计冻结快照。P2-01 新增 [agent_result.schema.json](agent_result.schema.json)，
用于 SDK 未验证结果和有界循环事件的实际出口校验；它不能授予数据库状态或独立验证权限。
Python SDK 的边界与运行说明见 [agent-sdk.md](../docs/agent-sdk.md)。

## P3 运行时补充

P3-01 的 CAS 发布在兼容 artifact.v1 的基础上绑定 producer_run_id、content_type 和
tool_schema_version；Producer/计划/Claim 与不可变元数据由 Go 控制面校验。
协议见 [Artifact 实现](../docs/artifact-context.md) 与 [ADR-0003](../docs/adr/ADR-0003.md)。

## P5 运行时补充

P5-01 按 07.F 补全 evidence.v1 的生产者、重放条件与预期观察字段，并实现严格类型和来源校验。
这补全此前未投入运行的设计样本；历史 P0 快照保存在 Git，细节见 ADR-0006。
Verdict 的 checks 不接受 Agent/Reviewer 自行宣告通过。
