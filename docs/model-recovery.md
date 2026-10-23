# 模型调用未知与验证后恢复（P4-05）

## 持久化边界

控制面 `ReserveModelCall` 在既有 workflow → budget → task 锁序内检查
Worker owner/epoch/revision、有效租约、当前计划和固定 ContextSnapshot，原子预留
调用数、Token 上界、微美元费用上界，并写入 `model_calls`、审计事件和 Outbox。
事务提交后调用方才可发送请求；提交报错（包括结果不明）不能作为重试发送依据。
同一 Run 的 logical_call_id 不可重复预留，同一 Task 有 reserved/unknown 时也不能换 ID 重发。
本批不改变 DDL，使用已冻结的 reservation/settlement JSON 字段。

`FinishModelCall` 只供可信 transport 记账，不接受模型自行声明的 usage。
已确认 usage（含已确认的失败费用）才从 reserved 转入 settled，精确到微美元。
显式 unknown、远端响应丢失或 Worker 失去租约都保留完整预留。
unknown 的 `usage={}`、`usage_known=false`、`usage_source=conservative_bound`，
其零值输入字段不代表观测到零用量；不会写成功、结算零费用或释放上界。
实际用量超出预留时拒绝结算并保留预留，后续按未知账目处理，不能静默截断。
Worker 合法续租可更新 revision；reservation 绑定 owner/epoch，不绑定旧 revision。
迟到结算、跨任务写入、重复结算及等待数据库锁期间租约过期均拒绝。
Task 有 reserved/unknown 调用时，两个产物提交入口都拒绝产生 output_ready。

恢复时，`ReconcileCalls` 在可信编排租约下从 PostgreSQL 读取事实。
有有效 Worker 租约、当前计划和可证明 reservation 的调用继续等待。
失效或不能证明的 reserved 转为 unknown；账本状态与审计/Outbox 同事务提交，
预算不变；恢复重跑不重复记账。unknown 或未确认的非幂等工具使 Workflow interrupted。
`ResumeWorkflow` 与 Task 接管继续拒绝未知副作用；恢复代码没有 Provider 发送入口。

已完成模型结算且有独立 verdict 的产物，在编排器崩溃后经
`ApplyVerdicts`、`FinalizeWorkflow` 完成 verifying → succeeded，直接引用原产物。
恢复不发送模型请求，也不重新请求验证。汇总依然检查当前计划与最新 verdict。

## 人工处理路径

1. 保持 Run interrupted，记录 run_id、call_id、logical_call_id、Provider 请求标识、
   reservation、usage_source 和对应 trace。请求标识缺失本身不能证明未执行。
2. 由可信操作员核对 Provider 的请求日志、账单及实际响应；对照固定输入快照和时间窗口。
   没有独立证据时继续保留调用数、Token 和费用上界，不重发、不置零、不强制 resume。
3. 若取得可靠证据，先保存可审计的处理结论。当前批没有提供人工强制结算/清账 API；
   后续预算和运维任务需提供受授权、带 CAS 与审计的处理入口，再显式恢复。
   不允许通过直接改 SQL 清零账本绕过这条边界。

## 验证与范围

`go test ./internal/handoff/... ./tests/chaos/... -run 'Unknown|VerifiedRecovery'`
使用真实 PostgreSQL、独立进程、独立 session/process group 和 SIGKILL。
H11/F-F 在本机 HTTP fixture 收到请求但不返回时杀死 Worker；恢复后真实请求计数仍为 1，
unknown 仍预留 1 次、64 Token、0.100000 美元估计（测试账本数字，无实际收费）。
H12/F-G 杀死持有编排租约且已落 verdict 的进程；新 epoch 恢复，模型请求和独立
fixture checker 各为 1，产物/模型调用/verdict 各只有一条。
其他测试覆盖跨进程并发预留、准入上界、回滚、迟到事务、租约与输出保护。

这些是离线合成测试，不是付费模型实验。当前仅准入 mock Provider；生产配置仍不开放付费调用。
P2 Python Provider 到该 Go 账本的自动 Worker 调度桥接、P5 独立 Verifier 服务、
P6 全局预算与人工结算入口均不在本卡。不得把已有 fixture 或恢复 API 宣称为完整自动系统。
网络分区、磁盘损坏、多主机故障不在本批 SIGKILL 验证范围内。
