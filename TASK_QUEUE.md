# TASK_QUEUE · 唯一任务状态清单

状态值：`TODO | ACTIVE | PASS | PARTIAL | BLOCKED | FAIL | NOT_RUN`。只在有 evidence 时改为 PASS。

| 卡号 | 阶段 | 标题 | 依赖 | 状态 | Evidence |
|---|---|---|---|---|---|
| [P0-01](tasks/P0-01.md) | P0 | 冻结边界、ADR 与契约索引 | 无 | PASS | `evidence/batches/P0-01/accept.json` |
| [P0-02](tasks/P0-02.md) | P0 | 仓库脚手架与依赖锁定 | P0-01 | PASS | `evidence/batches/P0-02/accept.json` |
| [P0-03](tasks/P0-03.md) | P0 | 严格测试/证据流水线 | P0-02 | PASS | `evidence/batches/P0-03/accept.json` |
| [P1-01](tasks/P1-01.md) | P1 | 数据库迁移与状态机 Schema | P0-03 | PASS | `evidence/batches/P1-01/accept.json` |
| [P1-02](tasks/P1-02.md) | P1 | 版本化 DAG 校验与原子 Plan Commit | P1-01 | PASS | `evidence/batches/P1-02/accept.json` |
| [P1-03](tasks/P1-03.md) | P1 | 依赖就绪、Fan-out/Fan-in 和取消传播 | P1-02 | PASS | `evidence/batches/P1-03/accept.json` |
| [P1-04](tasks/P1-04.md) | P1 | 最小任务 Claim / Lease / Epoch / Revision | P1-03 | PASS | `evidence/batches/P1-04/accept.json` |
| [P1-05](tasks/P1-05.md) | P1 | Run 生命周期、恢复扫描与 CLI | P1-04 | PASS | `evidence/batches/P1-05/accept.json` |
| [P2-01](tasks/P2-01.md) | P2 | Python Agent SDK 与受控 Loop | P1-05 | PASS | `evidence/batches/P2-01/accept.json` |
| [P2-02](tasks/P2-02.md) | P2 | 模型 Provider、解析与有限重试 | P2-01 | PASS | `evidence/batches/P2-02/accept.json` |
| [P2-03](tasks/P2-03.md) | P2 | ResearchAgent：来源约束的多轮检索 | P2-02 | PASS | `evidence/batches/P2-03/accept.json` |
| [P2-04](tasks/P2-04.md) | P2 | DataAgent：只读 SQL 与数值证据 | P2-03 | PASS | `evidence/batches/P2-04/accept.json` |
| [P2-05](tasks/P2-05.md) | P2 | ActionAgent：测试环境内受控操作 | P2-04 | PASS | `evidence/batches/P2-05/accept.json` |
| [P3-01](tasks/P3-01.md) | P3 | 内容寻址 Artifact Store 与身份 | P2-05 | PASS | `evidence/batches/P3-01/accept.json` |
| [P3-02](tasks/P3-02.md) | P3 | Context Snapshot 与依赖授权 | P3-01 | PASS | `evidence/batches/P3-02/accept.json` |
| [P3-03](tasks/P3-03.md) | P3 | Context Selector 与 Token/字节预算 | P3-02 | PASS | `evidence/batches/P3-03/accept.json` |
| [P3-04](tasks/P3-04.md) | P3 | 依赖追踪、过期输入与增量复用 | P3-03 | PASS | `evidence/batches/P3-04/accept.json` |
| [P4-01](tasks/P4-01.md) | P4 | Transactional Outbox + Inbox | P3-04 | TODO | `evidence/batches/P4-01/accept.json` |
| [P4-02](tasks/P4-02.md) | P4 | Dynamic Replanner 的有限计划增量修改 | P4-01 | TODO | `evidence/batches/P4-02/accept.json` |
| [P4-03](tasks/P4-03.md) | P4 | Fan-in Merge 与冲突/失败传播 | P4-02 | TODO | `evidence/batches/P4-03/accept.json` |
| [P4-04](tasks/P4-04.md) | P4 | 跨 Agent 故障注入与恢复扫描 | P4-03 | TODO | `evidence/batches/P4-04/accept.json` |
| [P4-05](tasks/P4-05.md) | P4 | 未知模型副作用与验证后收尾恢复 | P4-04 | TODO | `evidence/batches/P4-05/accept.json` |
| [P5-01](tasks/P5-01.md) | P5 | 证据契约与独立 Verifier | P4-05 | TODO | `evidence/batches/P5-01/accept.json` |
| [P5-02](tasks/P5-02.md) | P5 | 证据重执行与独立 Oracle | P5-01 | TODO | `evidence/batches/P5-02/accept.json` |
| [P5-03](tasks/P5-03.md) | P5 | Tool Gateway 与 Capability Policy | P5-02 | TODO | `evidence/batches/P5-03/accept.json` |
| [P5-04](tasks/P5-04.md) | P5 | 真正隔离的 Sandbox Executor | P5-03 | TODO | `evidence/batches/P5-04/accept.json` |
| [P5-05](tasks/P5-05.md) | P5 | 审批、风险分类与 Reviewer 辅助 | P5-04 | TODO | `evidence/batches/P5-05/accept.json` |
| [P6-01](tasks/P6-01.md) | P6 | 全局预算账本与原子预留 | P5-05 | TODO | `evidence/batches/P6-01/accept.json` |
| [P6-02](tasks/P6-02.md) | P6 | 资源池与 Backpressure | P6-01 | TODO | `evidence/batches/P6-02/accept.json` |
| [P6-03](tasks/P6-03.md) | P6 | 关键路径调度与成本感知模型路由 | P6-02 | TODO | `evidence/batches/P6-03/accept.json` |
| [P6-04](tasks/P6-04.md) | P6 | 端到端 Trace 与事件一致性 | P6-03 | TODO | `evidence/batches/P6-04/accept.json` |
| [P6-05](tasks/P6-05.md) | P6 | 运维命令与负载诊断 | P6-04 | TODO | `evidence/batches/P6-05/accept.json` |
| [P7-01](tasks/P7-01.md) | P7 | 合成任务集、独立 Oracle 与冻结清单 | P6-05 | TODO | `evidence/batches/P7-01/accept.json` |
| [P7-02](tasks/P7-02.md) | P7 | 端到端演示与合成业务完整闭环 | P7-01 | TODO | `evidence/batches/P7-02/accept.json` |
| [P7-03](tasks/P7-03.md) | P7 | 公平单 Agent / 固定 / 动态基线 | P7-02 | TODO | `evidence/batches/P7-03/accept.json` |
| [P7-04](tasks/P7-04.md) | P7 | 消融与统计分析报告 | P7-03 | TODO | `evidence/batches/P7-04/accept.json` |
| [P7-05](tasks/P7-05.md) | P7 | 压力、并发和单机混沌实验总验收 | P7-04 | TODO | `evidence/batches/P7-05/accept.json` |
| [P7-06](tasks/P7-06.md) | P7 | 真实模型有限预算评测与对账 | P7-05 以及用户显式实时模型授权；无授权则 BLOCKED/NOT_RUN | TODO | `evidence/batches/P7-06/accept.json` |
| [P8-01](tasks/P8-01.md) | P8 | 安全与一致性审计 | P7-06 | TODO | `evidence/batches/P8-01/accept.json` |
| [P8-02](tasks/P8-02.md) | P8 | 干净环境复现与最终发布门禁 | P8-01 | TODO | `evidence/batches/P8-02/accept.json` |
| [P8-03](tasks/P8-03.md) | P8 | 面试 Demo 与项目证据包 | P8-02 | TODO | `evidence/batches/P8-03/accept.json` |

**备注：** 每张卡只应在单次 Codex 会话实施；如失败继续同一张，禁止跳卡。任务 `P7-06` 需要额外用户付费授权；无授权保持 BLOCKED/NOT_RUN，不应阻塞工程版发布但阻塞“真实模型实验完全验收”的声明。

**特别门禁**：P7-06 唯一可在用户不授权付费时以 `NOT_RUN` 进入 P8 的工程审计；P8 只能签发 `ENGINEERING_ONLY`。其他卡必须 PASS 才前进。

**P0-01 验收说明：** 设计冻结与离线静态检查 PASS（37 项检查、24 个测试）；运行时能力当前未实现。
本卡未运行 Go/数据库/沙箱/真实模型验收，未进行付费调用。异常状态未定义的转移默认拒绝，后续实现前须先受审 ADR。
P0-01 的原始未提交快照与失败记录保留于 `evidence/batches/P0-01/accept.json`；之后已本地提交（`a1f06b1`）。
上一批用户授权先补齐 P0-02、P0-03，再依次完成 P1；该批未进入 P2。各行状态以对应批次证据为准。

**P2 授权：** 用户要求开始 P2；逐卡完成 P2-01 至 P2-05 并记录证据，不进入 P3，不启用付费模型。

**P3 授权：** 用户继续要求完成 P3；逐卡验收 P3-01 至 P3-04，不进入 P4，不启用付费模型。

**P0～P3 复查授权：** 用户要求检查已完成工程并修复不符合任务书或可能导致漏洞的代码。
本轮状态 PASS；范围 P0-01～P3-04，历史逐卡记录不覆盖，补充证据为 `evidence/batches/P0-P3-AUDIT/accept.json`。
复查与缺陷映射见 [P0～P3 工程复查](docs/audits/P0-P3.md)，不进入 P4。
