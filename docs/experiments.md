# 实验方法与结果索引

当前数据为 **E0 Mock 工程实验**，不能据此声称真实大模型准确率或策略优势。
P7-03 的 420 次运行与冻结 Oracle 保持只读。P7-04 从原始 JSONL、调用账本、
压缩报告和计时记录生成统计，不读已有 summary 作为数值真值。

## P7-04 冻结统计口径

- 每题每组一个分配项；成功率分母包含失败、超时、预算耗尽和拒绝题。
- answer decision=answer 视为提出答案声明；Oracle 不通过计 unsupported answer claim。
  这与严格 Workflow succeeded 分开，不能把未验证的基线完成称为 Workflow 成功。
- 实验时延采用 monotonic_ns 至原始记录 fsync，包含环境准备和外部 Oracle；严格
  Workflow 时延采用数据库 created_at→finished_at。两者分别给出 P50/P95，单位 ms。
  分位数为排序后 (n−1)p 线性插值；缺失值有覆盖率，不用零补齐。
- paired delta 为右组减左组，同 case_id 配对。固定 seed=20261025，5000 次有放回
  重采样，百分位 95% CI。缺少配对直接拒绝，不取交集丢样本。此区间仅描述当前
  冻结题集的变化，不能推广为真实模型采样不确定性或据此宣称显著提升。
- Token 使用账本 local_stub 的 UTF-8 字节/4 估算，价格 demo-free.v1。缺 Usage 的
  总量为 N/A，已知部分与预留上界单列；零调用的 coverage 为 N/A。
- 候选选择关键证据保留率按实际 admitted/rejected 身份和 kind 计算。严格组未
  导出逐次 selector 记录时此指标为 N/A；最终引用身份正确率与业务角色证据覆盖
  另按原始产物/引用计算。失败或拒绝没有最终引用时明确 N/A，不伪造 100%。
- B0 无跨 Agent 交接，其引用覆盖为 N/A。B3/A2 为相同处理的独立运行；若全部
  关键证据都保留且 token 没有节省，报告原始结果。
- 七组共用预算上界，同时列出实际调用/Token/费用。执行顺序未随机化且每题仅
  运行一次，实际耗时受冷启动及运行顺序影响，只给描述性比较。

```bash
source scripts/dev-env.sh
python -m pytest -q tests/unit/test_metrics.py
python eval/analysis/report.py --input evidence/eval/offline --output evidence/eval/reports
```

输入 index 固定两份 P7-03 正式运行目录及每个文件 SHA256；报告拒绝内容变化、
丢失分配、重复样本、跨组配置/题目/namespace 不一致。输出目录必须不存在，避免
覆盖报告。输入与输出均不能指向外部文件或符号链接。生成 JSON/CSV/Markdown/SVG，
所有统计使用程序计算，图表采用锁定版本 Matplotlib。

真实模型评测仍由 P7-06 接入并在模型/密钥/费用上限明确后执行。

P7-04 产物：[`report.md`](../evidence/eval/reports/report.md)、
[`metrics.json`](../evidence/eval/reports/metrics.json)、
[`comparison.svg`](../evidence/eval/reports/comparison.svg)。
输入索引、输出文件哈希以及每题/每类/配对 CSV 一并归档。图中 CI 使用上述冻结口径。

## P7-05 单机工程总验收

`bash scripts/test.sh --full` 执行全部启用的工程测试（真 PostgreSQL、独立进程、
Docker 沙箱、race、pytest、静态检查），并检查无 FAILED/SKIPPED/XFAILED。
此开关的范围是 ENGINEERING_ONLY，真实模型实验仍须单独授权。

`bash scripts/kill_matrix.sh --set all` 覆盖 F-A～F-H。每个窗口记录实际子进程
PID、SIGKILL 退出状态、死亡前后数据库快照（任务版本/epoch、产物、预算、事件），
再匹配同 Run 的恢复证据。F-D 使用生产 CAS 内部测试钩子，准确停在 temp fsync
之后、rename/元数据提交之前；崩溃后验证孤儿可识别、不可作为完成产物读取，接管后
旧 owner 写入被拒。其他窗口复用既有真 PG 跨进程故障测试，不把终止 goroutine 算作 SIGKILL。

`bash scripts/eval.sh --offline-load` 固定依次测试 1/4/8/16 个独立 **Go 测试 Worker**。
每进程执行四个任务，共 116 个任务、29 个 Workflow；同一档位共享真实 PG Schema
及 llm/db/cpu 资源池，容量最多为 min(GOMAXPROCS,4)。每个子进程只有一个数据库连接、
GOMAXPROCS=1。运行前至少保留 2 GiB cgroup 内存余量；子进程最长 90 秒，每档 120 秒，
Go harness 240 秒。不存在无限压测或外部模型请求。

负载是持久 Claim→Snapshot→本地 Model Stub 预算预留/结算→固定只读 SQL→CPU 哈希
计算→Artifact 输出，父控制器用独立算术真值验证，再通过权威 API 完成 Workflow。
不使用 sleep 模拟工作量；重复提交被拒、产物数/调用数/终态事件数准确、预算守恒、资源
全部释放均是硬断言。此负载度量控制面，Python Agent / Docker 修复 / 真实 LLM 的完整
闭环另见 P7-03 和后续 P7-06，不能把本实验吞吐作为它们的吞吐。

报告提供每任务 claim→output_ready 的真实 monotonic P50/P95（线性分位数、ms），
Worker 阶段和含独立验证的两种 tasks/s，以及每 10 ms 采样的队列深度、各类持有数、
PG 连接/活动连接/未授予锁和数据库尺寸。采样可能漏掉短峰；PG 统计包含同一合成库的
其他活动。记录完整硬件/cgroup、版本、源码指纹、每个任务与 Workflow 账本，不强设
吞吐提升阈值。每档单次有界试验、顺序未随机化，只能作为当前宿主描述性基线。

所有运行写入新的 `evidence/eval/engineering/{load,kill}-<id>/`，失败也保留。
Go 长日志按 Test 重新拼接再解析，缺失任一并发档、死亡窗口或恢复记录都不能 PASS。
不声称验证跨主机断电、物理机器故障、生产流量或真实付费模型。

## P7-06 真实模型准备状态

截至 2026-10-25，[配置提案](../eval/live/plan.json) 固定 24 题 × B0/B1/B2/B4 共 96 项，
全部 NOT_RUN。模型、官方单价版本、用户美元上限尚未确定，费用估计 N/A；环境未配置密钥，
候选 Provider 域名尚未获网络放行。未发任何真实模型请求。

实际 CLI 的非 mock 拒绝已复现。Provider 接入 Go 持久预算/实验层仍需实现，不能将现成
Python 适配器视作完整闭环。范围与验收拆分见 [ADR-0016](adr/ADR-0016.md) 和
[CHANGE_REQUEST](../CHANGE_REQUEST.md)，等待用户确认；P7-06 父卡为 BLOCKED。
