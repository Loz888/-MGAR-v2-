# MGAR v2 · Multi-Agent Orchestration Platform

**Codex 分批实施任务书 · 版本 2.0 · 2026-10-22**

> 文档用途：指导 Codex **每次只完成一个任务卡**，通过分层验收，将项目逐步构建为可独立运行、真实协作、故障可恢复、结果可验证、有完整实验的多智能体平台。本包是**实施规格而非已实现代码**；任何通过/性能/效果须以实际测试证据为准。

## 0. 如何使用（先读）

1. 新建独立仓库 `mgar-v2`，保留原 `durable-coding-agent-runtime-source` 作为另一个独立项目。不要把这份任务书误认为对上一个项目的改动指令。
2. 首先阅读 `01_PROJECT_CHARTER.md`、`02_ARCHITECTURE_CONTRACTS.md`、`03_CODEX_WORK_PROTOCOL.md`、`04_ACCEPTANCE_MATRIX.md`、`05_EVALUATION_PROTOCOL.md`。
3. 将**整个任务书包原样解压到新仓库根目录**（`AGENTS.md`、`TASK_QUEUE.md`、`01_*.md`、`tasks/` 等路径不要移动）。Codex 从 `P0-01` 开始，每次只接收一个 `tasks/P*-*.md`。项目实现代码放在独立的 `cmd/`、`internal/`、`agentkit/` 等目录，业务文档另建 `docs/`。
4. 每批结束，Codex 必须给出变更文件、测试命令及真实退出码、证据路径、未解决风险、Git commit（若实际提交）。没有证据 = 未通过。
5. **禁止一口气执行整个任务书。** 只有上一个任务卡验收通过、用户确认进入下一批，才交付下一张任务卡。**唯一例外**：P7-06 因用户尚未授权付费模型而保持 `NOT_RUN` 时，允许在显式标注 `ENGINEERING_ONLY` 的前提下进入 P8 工程发布审计，但绝不可宣称真实模型实验完成。Codex 不得擅自跳到后面的阶段。
6. 最终发布需要满足 `08_RELEASE_GATE.md` 的工程门禁、E2E、混沌测试、权限测试、真实模型实验和可复现文档；无法运行的实验一律 `NOT_RUN`，不能宣称全部完成。

## 1. 项目一句话定位

**Go 控制面 + Python Agent 执行层**，让异构 Agent 对复杂任务进行动态 DAG 规划、上下文选择、可靠交接、证据验证、安全工具执行与成本感知调度。平台本体而非某个应用才是项目主角。

### 与原 Coding Agent Runtime 的区分

| 原项目：单 Agent 执行可靠性 | 新项目 MGAR v2：多 Agent 协作系统 |
|---|---|
| Worker Lease/Epoch、Checkpoint、Patch 恢复 | Dynamic DAG、跨 Agent Handoff、Plan Version |
| 单一 Coding Agent 的执行生命周期 | Research/Data/Action/Verifier 等异构角色的协同 |
| Worktree 和修改前后 SHA | 不可变 Artifact、Provenance、输入快照、依赖失效 |
| 单 Run 故障注入 | 跨 Agent Fan-out/Fan-in、局部重规划、故障传播 |
| 单 Agent 模型/工具预算 | 全局模型路由、负载控制、Token/成本归因 |

**避免技术重复的硬规则：** 新项目只实现编排必需的最小持久化执行适配接口；不再从头做一套复杂 Coding Agent Runtime、Patch Engine 或 Agent 循环历史。可以用可选执行适配器调用原项目；新项目必须在独立仓库中用本地执行适配器也能独立运行。

## 2. 最终可演示的完整能力（终态验收，不是当前状态）

- [ ] 真实 LLM 规划产生 **合法、有界、带版本的 DAG**；错误计划可拒绝或局部重规划。
- [ ] Research、Data、Action 三类 Agent 至少各有真实工具决策循环；Verifier 能独立验证证据，不能用模型自评代替事实。
- [ ] 并行分发、Fan-in、任务依赖解锁；任意 Agent 的输入来自**授权的、不可变的、版本固定的 Artifact 快照**。
- [ ] 交接通过 Outbox/Inbox 可靠投递；重复通知不能产生重复执行授权或重复提交；未知副作用不会盲目重放。
- [ ] 任务和计划在崩溃后可恢复；局部重规划使过期依赖失效，不把所有已完成任务无脑重跑。
- [ ] Role/Capability 检查在可信服务端发生；未知仓库与命令在无凭据受限沙箱执行。
- [ ] 模型/工具调用有预算、身份、审计、超时、并发限制；观察可溯源到 Workflow/Task/Agent/Artifact。
- [ ] 可运行可复现的正常、错误、越权、故障注入实验；Mock/离线与真实模型结果严格分开。
- [ ] 至少提供单 Agent、固定多 Agent、动态多 Agent 的公平对照与特性消融，保留失败样本。

## 3. 项目核心场景（统一贯穿所有阶段）

**合成订单服务异常调查与修复：** 一个受控的 Go 示例仓库 + PostgreSQL 合成订单/指标数据 + 变更说明文档。用户要求定位“订单聚合重复计数、服务延迟异常”的原因，形成有证据的修复建议并在**测试沙箱**验证。`ResearchAgent` 检查文档、日志/代码；`DataAgent` 在只读库核查数据；`ActionAgent` 在临时工作树中提出受限补丁并运行测试；`Verifier` 对实际测试和 SQL 证据重新验证。Planner 根据反馈形成 DAG / 局部重规划，Aggregator 最终输出事实、置信边界和可复现证明。

**禁止将此场景中的修复写入真实用户代码仓库或生产数据库；默认全部在合成、可抛弃环境执行。** 附加第二种不需要 Coding 的分析场景，证明平台并非只能处理代码修复。

## 4. 分层地图

| 阶段 | 主线 | 必须证明 |
|---|---|---|
| P0 | 工程边界、仓库、测试基座 | 干净环境可启动，所有外部动作默认关闭 |
| P1 | Durable Workflow + DAG | 不丢、不重、依赖正确、版本正确 |
| P2 | 实际 Agent Loop + 工具 | 真正做工具决策，不是固定脚本换名 |
| P3 | Artifact / Context Engineering | 内容寻址、来源可查、快照一致、按需上下文 |
| P4 | Handoff + Dynamic Replanning | 可靠传递、局部失效、局部重算、跨 Agent 崩溃恢复 |
| P5 | Verifier + Security + Sandbox | 错误声明被拒，越权被拒，危险执行被隔离 |
| P6 | Cost-aware Scheduling + Observability | 有界配额、资源调度、全链路成本/时延可解释 |
| P7 | Dataset + 实验 + 消融 | 有公平对照、真实实测、失败保留、可复现 |
| P8 | 发布审计与简历证据 | 从零部署验证、独立审计、边界与证据一致 |

各阶段任务卡路径见 `TASK_QUEUE.md`，逐项任务卡见 `tasks/`。接口与数据一致性不变量见 `02_ARCHITECTURE_CONTRACTS.md`。

## 5. 最终工程交付清单

```text
mgar-v2/
├── AGENTS.md                       # Codex 工作约束
├── TASK_QUEUE.md                   # 唯一任务状态看板
├── cmd/{mgarctl,orchestratord}/    # Go 控制面 CLI/服务
├── internal/{store,planner,dag,scheduler,handoff,artifact,context,
│             policy,budget,observability,verifier}/
├── agentkit/mgar_agents/{core,research,data,action,provider}/
├── contracts/                      # 版本化 JSON Schema / 接口契约
├── migrations/                     # PostgreSQL 显式迁移（不可修改已发布迁移）
├── fixtures/{incident,analysis}/   # 确定性合成任务与 Ground Truth
├── tests/{unit,integration,e2e,chaos,security}/
├── eval/{cases,runner,analysis,baselines}/
├── deploy/{compose.yaml,Dockerfile.*}/
├── scripts/{preflight.sh,test.sh,accept.sh,demo.sh,eval.sh}
├── docs/{architecture,adr,operations,limitations,security,experiments}/
└── evidence/{batches,releases}/    # 机器可核验的验收记录，去除敏感数据
```

## 6. 推荐启动顺序

直接复制 `templates/START_NEXT_BATCH.md`，填入当前任务卡 ID，然后把该任务卡与核心规范交给 Codex。执行前先把任务状态写进 `TASK_QUEUE.md`；执行后核对命令、测试与产物，再决定下一批。

**注意：P7 中真实模型实验必须获得用户显式授权（密钥、费用上限与要执行的任务范围）。缺少授权可以完成所有离线工程实验，但不能把真实模型实验标为 PASS。**
