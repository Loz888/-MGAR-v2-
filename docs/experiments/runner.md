# P7-03B 离线公平 runner

已批准隔离层见 ADR-0014、ADR-0015。七组共享 demo-mock.v1、demo.v1 工具、
48,000 字节 / 12,000 估算 Token 上下文上限，以及每题冻结预算。配置文件
`eval/config/arms.yaml` 使用 JSON（YAML 子集），不需要额外 YAML 依赖。

```bash
source scripts/dev-env.sh
python -m pytest -q tests/unit/test_eval_runner.py
python eval/runner.py --provider mock --arms B0,B1,B2,B3,B4
python eval/runner.py --provider mock --arms A1,A2
python eval/runner.py --verify evidence/experiments/<run-directory>
```

每条默认运行覆盖全部 60 个离线题；`--cases` 仅用于诊断，报告显式记录子集。
仅接受 Mock；输入集、Oracle、题目预算不可覆盖。固定 seed 已用于生成冻结输入，
Mock 决策为确定性工具观察函数；UUID、调度顺序、实际耗时不要求相同。

B0 只有一个真实 Python Worker、一个累积上下文，拥有同等场景业务工具上界。
analysis 的所有组均不提供写工具。B1 固定模板；B2/B3/A2 的实际 Planner 输出
经 Go 校验后决定业务 DAG；实验承载 Plan 只提供身份与预算。B3/A2 是同处理的
独立重复组。B4/A1 走严格验证链，只有 B4 在负面验证后调用正式 Replan。

版本变化题先真实读取 prior_orders，再发布当前 orders。B4 仅重算受影响节点；
动态隔离组不能凭无验证候选复用证明，重新生成生产节点与候选；固定组保留旧
结果供 Oracle 评分。工具报错按照既有 Gateway 保守记录为 unknown，Worker 停止；
该调用的预算预留和失败样本不会删除。预算为零的题仍真实尝试准入并被拒绝。

外部数学 Oracle 从冻结答案独立评分。incident 还要独立沙箱重执行本次实际
工作树；不是读取 Agent 的“测试通过”声明。外部检查开销单列，不反馈给 Agent。
当前所有业务候选均可能是 Selector 必须保留的关键证据，因此 full/selected
可能同耗费；不保证预设性能提升。结果只能支持 Mock 工程结论。

运行目录创建后不可复用；assignment 先持久化，随后追加并 fsync 原始记录。
独立 schema、CAS、模型账本、工具账本、压缩原始输出、OTLP 投影均可核验。
技术失败同样保留分配记录与输出；CLI 非零表示基础设施/完整性问题，预期
业务失败本身不会令 runner 退出非零。程序崩溃的未完成分配会被 verify 拒绝。

资源清理仅针对本次拥有的合成 schema、reader、worktree 和 CAS。实验执行器
不提供崩溃恢复承诺；异常中断可能留下需要按拥有者核查的临时资源。
端到端实验时延包含外部 Oracle 与原始记录落盘；严格 durable Workflow 终态
时延另列，隔离基线为 null，不能把其 interrupted 状态当作严格 Workflow 失败。
Trace 是真实审计投影，零时长事件不能用于伪造物理执行时延。

正式数据路径和真实命令退出码写入 P7-03B evidence；统计推断属于 P7-04。
