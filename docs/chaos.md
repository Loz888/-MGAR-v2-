# 单机故障矩阵

Version: kill-matrix.v1

运行 `bash scripts/kill_matrix.sh --set core`。入口仅接受 core，不接受外部 PID 或路径。
所有子进程由当前测试 exec 启动并 setsid；注入前检查 PGID 等于该子进程 PID，且不等于父进程组。
仅对这个新建的进程组发 SIGKILL，并检查 WaitStatus 确认实际信号。失败时回收自己启动的子进程。
数据库只使用 internal/testdb 的随机合成 schema；另外创建 sentinel Workflow，逐字核对未被影响，
并检查 PostgreSQL 仍可查询。未执行宿主数据库、父 shell 或外部进程终止。

| 窗口 | 实际 barrier 与恢复断言 |
|---|---|
| F-A | 获得 Orchestrator、进入 planning 后，调用 PlanCommit 前暂停；kill 后 active version 仍 0，实际租约过期后新 epoch 接管 |
| F-B | Plan + Outbox commit 返回后、投递前暂停；kill 后新 Orchestrator 恢复扫描，plan_committed 的 Inbox processed 与两个 root ready 仅提交一次 |
| F-C | Task Claim、固定快照与 executing 已提交后暂停；实际 DB 时间判定租约过期，新 Worker 同 Task epoch 增加，快照 ID/hash 不变，委派不重复扣费 |
| F-E | Outbox 已标 published，消费者在 Inbox processed 更新的真实 PG trigger 锁处暂停；kill 回滚业务，重新消费后 processed + ready 原子提交，重复消息不重复解锁 |
| F-H | 旧 Worker 已有执行授权，新 Plan 通过 P4-02 取消旧实例并激活后 kill；重启进程携带旧 Claim 发布被 FENCED，新计划使用不同 Task ID |

进程间通过管道同步实际 API 返回边界，事务中窗口通过测试 schema 内的 PostgreSQL trigger/advisory
锁观察 `pg_blocking_pids`。不以固定 sleep 猜测崩溃时机；租约实验轮询真实数据库时间。
每个窗口日志包含 PID/PGID、旧/新 epoch、active plan、Workflow 状态和数据库 trace 事件。
新 Task 的 epoch 可从 1 开始；F-H 的防线是不同 Task ID + Plan Version，不伪造跨实例 epoch 递增。

P4-01 另有 H01–H04 的真实结果提交/投递窗口；P3 已有 CAS 临时文件窗口。
P4-05 的 F-F/F-G（对应 H11/H12）使用独立入口：
`go test ./internal/handoff/... ./tests/chaos/... -run 'Unknown|VerifiedRecovery'`。
它们在本机 HTTP 请求未返回、独立 fixture verdict 已落库的实际边界发送 SIGKILL，
检查 unknown 上界保留以及 Provider/验证请求均不重复；详情见 [模型恢复](model-recovery.md)。
这些结果仅说明本机 Linux、PostgreSQL、进程 SIGKILL 下的恢复行为，不代表跨主机或断电容错。
恢复路径读取数据库权威状态；没有在本卡新增外部通信、付费请求或自动 Python Worker。
