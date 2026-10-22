# 05 · 实验与论文级证据规范（不造数）

## 1. 三种证据层次

- **E0 工程**：MockProvider + deterministic fixtures + 真 PG / Docker / SIGKILL，验证状态机、权限、幂等、恢复；可全面离线运行。
- **E1 Agent 能力**：真实模型实际作决策/调用工具/反思，冻结任务集与独立 Oracle，测试成功率、错误成功声明、成本/时延。只有显式付费授权才允许执行。
- **E2 策略收益**：在相同或明确分组匹配的条件下比较单 Agent、固定编排、动态 DAG 的端到端效果，分析任务复杂度、并行度、上下文策略和验证成本。不能只展示最有利子集。

## 2. 冻结评测任务集

**工程集（默认离线）**：至少 60 个有种子的确定性 Workflow fixtures，覆盖 10 大行为维度：简单任务、并行、Fan-in、任务失败、部分重规划、Context 过期、乱序重复、预算不足、无解/拒绝、权限否决；每维 ≥6。Mock 的输出是脚本化模拟，不被算为“模型准确率”。

**模型集（显式授权）**：至少 24 个冻结任务，三类难度均衡（简单8、中等8、复杂8），至少覆盖 incident/analysis 两场景；每题有独立 Ground Truth/Oracle；至少 6 题包含负例或应拒绝目标。任务冻结后不能按模型输出修改正确答案；任何修改数据集必须提升版本并重新全量运行。

**保留集**：开发用于调 Prompt 的任务与最终报告评测任务严格分离。例如模型集 24 个不被用于反复针对性调整 Prompt；另建开发集若干任务。评测集可公开只在评测完成后公开报告。参数、种子、时间截断等固定记录。

## 3. 实验组与变量

| 组别 | 动态 DAG | 多角色 | Context Selection | Verifier | 目的 |
|---|---|---|---|---|---|
| B0 Single | 否 | 否 | 默认单上下文 | 同等最终外部 Oracle | 单 Agent 基线 |
| B1 Fixed Multi | 固定模板 | 是 | 全上下文（受相同预算） | 同等最终外部 Oracle | 多角色分工效果 |
| B2 Dynamic Multi | 是 | 是 | 全上下文 | 同等最终外部 Oracle | 动态规划增益 |
| B3 Dynamic + Context | 是 | 是 | 选择性输入 | 同等最终外部 Oracle | 上下文管理增益 |
| B4 Full | 是 | 是 | 选择性输入 | 内部独立 Verifier + 最终外部 Oracle | 完整系统 |
| A1 No Replan | 固定初始 Plan | 是 | 选择性输入 | 内部 Verifier | 局部重规划消融 |
| A2 No Internal Verifier | 是 | 是 | 选择性输入 | 最终外部 Oracle 不变 | 内部验证消融 |

**公平性要求**：固定相同模型版本/温度/上下文预算、Tool 能力上界、总调用与美元预算，记录每组因预算耗尽而失败的情况。对于不同架构天然耗费不同数量调用的事实，报告分别给出**等预算比较**和**实际耗费比较**；不得删掉超预算失败样本。

若真实模型 24 个任务×全部 7 组超过用户授权成本，优先完成 B0/B1/B2/B4 四组，然后 A1/A2；其余明确 `NOT_RUN`。不能静默降低样本数并宣称完整实验。

## 4. 指标与计算口径

- `task_success_rate = independent_oracle_pass / all_assigned_tasks`（包含失败、超时和预算耗尽）
- `unsupported_success_claim_rate = false_success_claims / total_success_claims`；若分母=0，显示 N/A 而不是 0
- `workflow_success_rate`：每个 Workflow 全部必要条件满足且证据有效，不能只凭最终模型说“完成”。
- `end_to_end_latency_ms`: submit 接收到 final durable terminal; P50/P95 报样本和测量仪器；模拟耗时与真实耗时分开。
- `model_calls`, `tool_calls`, `input_tokens`, `output_tokens`, `usage_coverage`；unknown 不记成 0。
- `estimated_cost_usd`：按被冻结的单价表估算，不是发票；注明 provider reported usage 和缺失数据上界。
- `plan_quality`: DAG 合法率、重复/多余任务比例、平均 Plan 版本数、重规划成功率。
- `parallelism`: 有效可并行任务量、任务关键路径长度、每 Workflow 最大并发、资源利用率。
- `context_efficiency`: 传递 Token、关键证据保留率、引用准确率、过期输入误用次数。
- `handoff_reliability`: 事件重复次数、去重次数、丢失事件数（重试窗口内）、错误完成次数。
- `recovery_correctness`: fault scenario verified_pass / attempted；每个故障点独立报告。
- `security_violation_count`: 越权执行成功次数（目标为 0）；“拒绝次数”另计避免误解。

## 5. 实验执行步骤

1. 冻结 `dataset_manifest.json`（case_id、类别、ground_truth_ref、fixture_sha、oracle_version、seed）；由与 Agent 输出独立的脚本构造答案。
2. 每组启动**干净的数据库 Schema 与 Artifact Namespace**，不要复用上一组写过的状态。
3. 记录模型请求配置、供应商返回的模型名称/版本标识、工具版本、Git SHA、机器资源、Docker image digest、DB 版本。
4. 每个任务执行时写 `runs.jsonl`（不可改写）、`model_calls.jsonl`, `tool_calls.jsonl`, `traces`, `oracle_judgments.jsonl`；异常完整保留，并对敏感内容脱敏。
5. 每组输出 `summary.json`, `per_case.csv`, `latency_histogram`, `failure_taxonomy.md`；算术使用明确脚本，不能让 LLM 读日志后凭记忆生成数字。
6. 单组结束后比较成功/失败案例并保存代表性 Trace；没有失败案例也要报告负例触发是否有效。
7. 样本小只报告描述统计和不确定性：可使用 paired bootstrap 95% CI（固定 seed、重复 5,000 次），禁止在 CI 重叠/不重叠上进行随意显著性断言。
8. 在 README 明确“已运行”“未运行”“实测”“推断”“已知边界”。

## 6. 真实模型的操作授权

**默认禁止**。只有用户明确允许、密钥可用，且设置三个条件才运行：

```bash
export MGAR_ALLOW_PAID=1
export MGAR_MAX_COST_USD='2.00'  # 示例，不是默认授权
export MGAR_LIVE_CASELIST='eval/cases/frozen-live-24.json'
```

运行前必须 preflight 打印冻结单价表、预计请求上界、预算上界、模型别名和风险，并要求 CLI `--confirm-live` 明确指定。模型调用的实际计费只能查询提供商账单；本项目展示估算和报告用量。**不允许用 Mock 自动填充 live 缺口。**

## 7. 数据分析与阈值

- 工程正确性：D/A/H/S/O 系列对应启用测试断言 **100% PASS**，任何不通过阻止发布。
- 真实模型：不预设“必须胜过基线”的结果阈值。成功率可能持平或下降，照实报告；演示工作流至少应该有一条经模型真实完成、经外部 Oracle 和 Verifier 核实的完整运行，否则不能宣称真实 Agent 闭环验证成功。
- 故障恢复：指定八个 SIGKILL 窗口每个有实际进程死亡记录和接管前后 DB evidence；全部通过才能称“已验证的单机故障恢复”。
- 性能：没有预设虚假的 QPS 目标；在已记录负载上给出真实基线与瓶颈分析。如果吞吐未提高，可以报告负效果，之后再针对瓶颈优化。

## 8. 最终报告骨架

1. 任务/数据集构成和冻结哈希；2. 环境、模型/工具/价格版本；3. 实验组与授权范围；4. 工程正确性；5. 端到端真实能力；6. 成功率/成本/时延结果表；7. 消融和反例；8. 故障注入与安全；9. Failure Taxonomy；10. 可复现命令；11. 限制和未验证项。
