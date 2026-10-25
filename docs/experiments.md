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
