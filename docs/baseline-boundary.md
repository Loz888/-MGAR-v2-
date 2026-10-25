# P7-03A 冻结接口 · baseline-boundary.v1

本契约实现用户批准的 ADR-0014 方案 A，不修改严格生产依赖语义。

## 候选证据 candidate_evidence.v1

信封由受信 Go 发布器从真实 CAS Artifact 与数据库来源生成：artifact_id、producer
Run/Task/role/node、source_plan_version、tool_version、kind、content_sha256、byte_length、
原始 content，以及固定 semantic_status=unverified。没有 verified=true 字段。
同内容的身份和来源不可合并；信封哈希也固定。模型不能指定权威 Metadata。

候选发布只接受本实验的当前计划节点、实际 output_ready 输出和零 verdict；
不调用 VerifyArtifact。发布前任务仍经正常 Claim/Start/Snapshot/Gateway/预算流程。
实验准入 Plan 的节点均无生产依赖；它只承载真实身份、工具权限和费用，不冒称业务 DAG。
业务编排由隔离实验调度器按冻结顺序执行，禁止把候选喂入严格 ReadInput/Ready 链。

## 授权与读取

Grant 为受信控制器 API，不对 Worker/模型暴露：绑定精确 consumer Claim（Run、Task、
Role、owner、epoch、revision、plan）、候选原始身份/哈希/版本列表。角色矩阵：
Action 可接 Research/Data；Aggregator 可接 Research/Data/Action；Research/Data 无上游。
同 consumer 的首次 Grant 固定内容，不允许事后换引用；未知 Grant 默认拒绝。

Worker 只能通过 credential-bound Gateway 的 candidate.read 读取本次固定上下文。
该读取也记入工具预算与资源队列；内容在 Provider 调用前交付并纳入实际请求摘要/用量。
每次读取核验当前 Claim、当前 Plan、来源记录、CAS 哈希、信封哈希，并在返回前再次
检查授权。无需新表；Grant 不跨重启恢复，未知/中断实验不自动重放。

## baseline_execution.v1

execution_status=completed/failed/blocked，semantic_status=unverified；不包含 Oracle
结论。workflow_success=null（N/A）。严格承载 Workflow 结束于 interrupted 或 blocked，
从不凭基线完成改为 succeeded；已发布候选保持 output_ready，验证记录为零。
外部 Oracle 在运行后独立读取冻结输入/答案评分，候选内容从不成为 Ground Truth。

每次输出保留 case、真实 Workflow 状态、预算账本、进程/工具记录、候选、错误、
实际审计投影和独立 CAS/schema namespace。失败仍有结构化记录；技术异常也不能删样本。
报告经调用者落盘后可清理本次拥有的合成资源；不宣称此临时实验支持崩溃恢复。

## 本卡边界

仅固定多角色参考路径，证明候选交接可执行且不会提升验证状态。B0 等七组的策略
选择、正式样本运行和统计在 P7-03B/P7-04 单独实现、验收。工具版本继续为 demo.v1，
模型 demo-mock.v1，Mock 单价 demo-free.v1；无付费路径和新增 DDL。
