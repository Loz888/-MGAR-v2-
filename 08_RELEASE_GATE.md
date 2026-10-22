# 08 · 发布门禁与简历成果审计

## R0 · 仓库和文档

- 全新 Linux/WSL2 工作目录，按 README 完成 `scripts/preflight.sh`, `scripts/bootstrap.sh`, `scripts/test.sh`, `scripts/demo.sh --provider mock`；若某脚本未实现则发布失败。
- 所有迁移干净安装和版本升级路径被测；环境变量模板不含真凭据；依赖版本和 Docker image 锁定。
- `docs/architecture.md`, `docs/protocols.md`, `docs/security.md`, `docs/operations.md`, `docs/limitations.md`, `docs/experiments.md` 与源码一致。

## R1 · 系统功能

- 支持用户提交复杂任务；动态生成可验证 DAG；多个异构 Agent 实际工具调用；并行、Fan-in、局部重规划；按依赖/权限传递版本化 Artifact；独立 Verifier；最终附证据汇总。
- 支持 `status / trace / cancel / resume / inspect-artifact`；同一 Workflow 崩溃后可恢复或诚实进入需要人工干预的状态。

## R2 · 工程质量

- Go 全量、Race、Vet；Python 全量、Lint、固定类型检查；正式迁移检查；至少 60 个离线工程/契约用例及矩阵中必须覆盖的负例均通过，0 FAIL / 0 SKIP / 0 XFAIL。
- 真 PG 多进程并发预算/Claim；八个真实 SIGKILL 窗口；沙箱越权/资源负例；E2E 两类业务均通过。
- 运行记录与 `git rev-parse HEAD` 对齐；未提交变更注明；未执行项明确 NOT_RUN，不能一笔带过。

## R3 · 科学评测

- E0 60 个离线工程/工作流 fixture 完成，且所有失败有记录；请注意它们不构成“LLM 成功率”。
- E1 真实模型集使用冻结的至少 24 个 case，完成与预算对应的已授权实验组，并逐项报告完成/NOT_RUN；若缺 live 授权，**工程发布可以进行但不得宣称『真实多智能体能力已完整实验验证』**。
- E2 对照至少 B0/B1/B2/B4（若 live 授权足以覆盖），说明 Agent 数量、context、verifier、replan 增益和成本代价；附完整 per-case 数据。

## R4 · 安全与运行边界

- 控制面凭据与沙箱隔离，服务端工具权限检查、SQL Reader Role、网络禁用与资源限制有真实负例。
- 报告不能以“安全沙箱”“生产级 exactly-once”“多机容错”“自动正确修复任何仓库”等超出证据范围的词汇做主张。
- 对模型输出攻击、越权资源引用、预算竞争、未知副作用重放有测试与已知边界。

## R5 · 面试展示包

最终输出：

1. 5–8 分钟 `scripts/demo.sh` 演示脚本（含正常、验证拒绝、进程崩溃后恢复三段）。
2. 架构图，指出确定性控制面/LLM Agent/沙箱/Artifact/Verifier 的信任边界。
3. 一页协议不变量：DAG 与计划版本、Outbox/Inbox、Epoch/Revision、Artifact/Context、Budget。
4. 真实数据表：Single / Fixed / Dynamic / Full，每项附任务数、模型成本与硬件条件。
5. 三个最深技术问题的问答：动态重规划如何局部失效；At-least-once 为什么不等于外部 Exactly-once；Verifier 怎样避免“模型评模型”。
6. 一页 Limitations，区分单机/多机、沙箱可信范围、数据集代表性与未知付费调用。

## 简历叙述（只有完成并被证据支持后才可填）

> **MGAR — 多智能体协作与执行平台（Go/Python/PostgreSQL/Docker）**：设计基于版本化 DAG 的动态编排与依赖感知调度，实现 Agent 间可靠 Handoff、内容寻址 Artifact 与权限感知 Context Selection；将独立证据重执行和 Tool Gateway 融入协作闭环；通过真实进程崩溃、并发竞争、权限负例与冻结任务集完成验证，量化单 Agent 与多 Agent 在正确性、时延和模型成本方面的差异。（填入**实际**指标，未实现模块删去。）
