# 调度与路由（P6-03）

`Scheduler.ClaimScheduled` 支持 `fifo` / `critical_path`，数据库视图来自当前计划；最终 `ClaimReady` 在事务内重验版本、角色、Ready 状态、依赖固定输入与预算。读视图后被重新计划/领取的任务会拒绝，不自动选择另一任务。原 `Claim` 保留恢复租约用途。历史值是本 run 成功任务的执行至产出事件间隔中位数；无数据默认 1 秒，估计裁剪 1ms–5min。Ready 等待秒数加入关键路径秒数，不能突破合法 Ready 边界。历史只影响性能，不影响正确性。

`budget.Router` 的配置由可信主机提供。角色来自已认证 TaskClaim；能力表仅筛选模型，不改变工具授权。只启用现有 mock Provider，不存在隐式付费回退。优先模型不能用时，需显式 `AllowFallback`；无偏好则选满足能力与预算的最低预留成本。`Resources.RoutedModel` 组合路由、资源池及持久预留；报价之后预算变化导致预留失败时不会调用 transport。解释随 `model_reserved` 原子入库，保留冻结价格版本。

复现：`go run ./cmd/schedulingbench --samples 10`。固定 DAG 包含 8 个独立 15ms 工作和一条 5×20ms 链，实际执行本地 timer 工作模拟等待型负载，并发 2；各类先真实执行 3 次获取耗时中位数，再交替策略顺序运行。JSON 包含每个任务 Ready/开始/结束时间、父依赖、主机信息与原始样本，时间为 ms，吞吐为 tasks/s，p50/p95 为每轮从工作流开始到任务完成的 nearest-rank 分位数；Jain 公平指标对 service/(service+Ready 等待) 计算，同时给出最大 Ready 等待。证据 `evidence/batches/P6-03/scheduling-report.json` 保存本轮实际结果。

这是固定本地负载实验，不代表真实模型服务/CPU 密集工作；结果受宿主调度影响，测试检查指标可重算及依赖/并发约束，不用不稳定的性能阈值制造 PASS。P7 才负责完整 Worker 执行链组装。未知执行占资源或任务时，不承诺有界等待。
