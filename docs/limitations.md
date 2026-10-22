# P1 阶段能力与限制

Version: limitations.v1

P1 实现的是单机持久化控制面：真实 PostgreSQL 约束与事务、版本化 DAG、
编排器和 Task 租约、预算预留、生命周期与恢复扫描。
当前验收针对 Linux / WSL2；其他操作系统的进程信号行为未验收。
具体生命周期决策见 [ADR-0002](adr/ADR-0002.md)，运行方法见 [development.md](development.md)。

租约到期统一由 PostgreSQL 的 `clock_timestamp()` 判断，Owner、Epoch、Revision 与当前计划版本
共同约束提交；旧 Owner 即使恢复运行，也必须通过持久化 fencing 检查。
数据库墙上时钟并非单调时钟：时钟回拨可能延长租约有效窗口，向前跳变可能使租约提前到期。
本阶段没有跨主机时钟协议、时钟跳变检测或硬件时间保证，依赖本机数据库时间的正常维护。

SIGKILL 验收使用真实独立进程和真实 PostgreSQL：在编排器租约已提交、恢复扫描尚未继续的
选定窗口，经管道屏障杀死进程，确认实际 SIGKILL，再启动新进程恢复。
实验仅对其拥有的合成测试租约注入过期时间，以确定性触发接管，避免用 sleep 假造竞争。
它验证该窗口的持久 checkpoint、Epoch 增长、ready 重建和陈旧写拒绝；
未覆盖每个指令位置，也未实验宿主断电、磁盘损坏、数据库崩溃或网络分区。
因此不承诺跨主机容错、宿主断电恢复时限或外部 Exactly-once。

`mgarctl` 和持有数据库凭据的 Go 进程属于本机受信控制面。
`submit --fixture incident --provider mock` 仅提交固定 A（research）+ B（data）→ C（report）
的离线设计 fixture，创建 pending 任务；ready 由后续 daemon 扫描重建，不运行 Worker。
`status`、`cancel`、`resume` 与 daemon 的恢复扫描
通过受控 Go 写路径处理状态，不能作为向非受信方开放的任意数据库接口。
当前没有完整多用户认证、授权服务或面向不受信 Agent 的 Capability Gateway。
`mgarctl health` 不需要数据库，不证明数据库可用或业务执行成功。
Daemon 启动依赖数据库连接及 Ping；HTTP `/healthz` 检查 PostgreSQL 连通性，
不可用时返回 503，健康响应也不证明业务执行完成。
连接配置仅允许 `127.0.0.1` / `localhost` 的 `55432` 端口，数据库名精确为
`mgar_test` 或 `mgar_test_` 后接 16 位小写十六进制字符。

恢复不会试探性重播未知模型调用或非幂等工具动作。
存在 `reserved` / `unknown` 模型调用，或与活动任务关联且结果不可安全重播的非幂等动作时，
控制面保守阻止自动恢复，并保留费用预留；取消也不代表外部动作被撤销或费用已退款。
委派次数与费用预留不能因重启、取消或旧计划清理自动退回。
达到尝试上限的过期任务安全失败；总委派预算耗尽且没有活动执行槽时可进入 `blocked`。
`resume` 仍须检查预算、当前状态和未知副作用；人工核对与结算流程未实现，无法消除不确定性时继续拒绝。

P1 的 PostgreSQL 与跨进程测试可以预置合成 Artifact 元数据和 append-only verdict，
用于验证控制面接入已存在受信记录后的状态转换、依赖、撤销和事务行为。
这些 fixture 不能证明内容真实、文件已持久化、验证者独立或真实 Agent 已完成任务。

以下能力尚未交付：

- P2 的 Agent Loop 与实际 Worker 执行闭环。
- P3 的 Artifact 内容寻址存储（CAS）、真实内容写入及其故障恢复。
- P5 的独立 Verifier 执行与完整信任边界验收。
- 真实模型与付费 Provider 调用、真实外部工具副作用和生产数据库接入。

默认只允许 mock Provider；离线验收不使用模型密钥，不授权付费调用。
阶段证据仅对应实际执行的命令和覆盖窗口，见 [`evidence/batches/P1-05/`](../evidence/batches/P1-05/)。
