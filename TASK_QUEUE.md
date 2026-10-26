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
| [P4-01](tasks/P4-01.md) | P4 | Transactional Outbox + Inbox | P3-04 | PASS | `evidence/batches/P4-01/accept.json` |
| [P4-02](tasks/P4-02.md) | P4 | Dynamic Replanner 的有限计划增量修改 | P4-01 | PASS | `evidence/batches/P4-02/accept.json` |
| [P4-03](tasks/P4-03.md) | P4 | Fan-in Merge 与冲突/失败传播 | P4-02 | PASS | `evidence/batches/P4-03/accept.json` |
| [P4-04](tasks/P4-04.md) | P4 | 跨 Agent 故障注入与恢复扫描 | P4-03 | PASS | `evidence/batches/P4-04/accept.json` |
| [P4-05](tasks/P4-05.md) | P4 | 未知模型副作用与验证后收尾恢复 | P4-04 | PASS | `evidence/batches/P4-05/accept.json` |
| [P5-01](tasks/P5-01.md) | P5 | 证据契约与独立 Verifier | P4-05 | PASS | `evidence/batches/P5-01/accept.json` |
| [P5-02](tasks/P5-02.md) | P5 | 证据重执行与独立 Oracle | P5-01 | PASS | `evidence/batches/P5-02/accept.json` |
| [P5-03](tasks/P5-03.md) | P5 | Tool Gateway 与 Capability Policy | P5-02 | PASS | `evidence/batches/P5-03/accept.json` |
| [P5-04](tasks/P5-04.md) | P5 | 真正隔离的 Sandbox Executor | P5-03 | PASS | `evidence/batches/P5-04/accept.json` |
| [P5-05](tasks/P5-05.md) | P5 | 审批、风险分类与 Reviewer 辅助 | P5-04 | PASS | `evidence/batches/P5-05/accept.json` |
| [P6-01](tasks/P6-01.md) | P6 | 全局预算账本与原子预留 | P5-05 | PASS | `evidence/batches/P6-01/accept.json` |
| [P6-02](tasks/P6-02.md) | P6 | 资源池与 Backpressure | P6-01 | PASS | `evidence/batches/P6-02/accept.json` |
| [P6-03](tasks/P6-03.md) | P6 | 关键路径调度与成本感知模型路由 | P6-02 | PASS | `evidence/batches/P6-03/accept.json` |
| [P6-04](tasks/P6-04.md) | P6 | 端到端 Trace 与事件一致性 | P6-03 | PASS | `evidence/batches/P6-04/accept.json` |
| [P6-05](tasks/P6-05.md) | P6 | 运维命令与负载诊断 | P6-04 | PASS | `evidence/batches/P6-05/accept.json` |
| [P7-01](tasks/P7-01.md) | P7 | 合成任务集、独立 Oracle 与冻结清单 | P6-05 | PASS | `evidence/batches/P7-01/accept.json` |
| [P7-02](tasks/P7-02.md) | P7 | 端到端演示与合成业务完整闭环 | P7-01 | PASS | `evidence/batches/P7-02/accept.json` |
| [P7-03](tasks/P7-03.md) | P7 | 公平单 Agent / 固定 / 动态基线 | P7-02 | PASS | `evidence/batches/P7-03/accept.json` |
| [P7-03A](tasks/P7-03A.md) | P7 | 隔离基线候选证据与执行层 | P7-02 | PASS | `evidence/batches/P7-03A/accept.json` |
| [P7-03B](tasks/P7-03B.md) | P7 | 七组公平 runner 与失败保留 | P7-03A | PASS | `evidence/batches/P7-03B/accept.json` |
| [P7-04](tasks/P7-04.md) | P7 | 消融与统计分析报告 | P7-03 | PASS | `evidence/batches/P7-04/accept.json` |
| [P7-05](tasks/P7-05.md) | P7 | 压力、并发和单机混沌实验总验收 | P7-04 | PASS | `evidence/batches/P7-05/accept.json` |
| [P7-06](tasks/P7-06.md) | P7 | 真实模型有限预算评测与对账 | P7-05、P7-06A/B/C | ACTIVE | `evidence/batches/P7-06/accept.json` |
| [P7-06A](tasks/P7-06A.md) | P7 | 可信 Provider 与持久账本接通 | P7-05 | PASS | `evidence/batches/P7-06A/accept.json` |
| [P7-06B](tasks/P7-06B.md) | P7 | 冻结 live runner 与总预算 | P7-06A | PASS | `evidence/batches/P7-06B/accept.json` |
| [P7-06D](tasks/P7-06D.md) | P7 | 官方 tokenizer 与可证明输入预算上界 | P7-06B、官方 tokenizer 数据 | PASS | `evidence/batches/P7-06D/accept.json` |
| [P7-06C](tasks/P7-06C.md) | P7 | 真实执行、独立评价与对账 | P7-06B、可证明的输入预算上界 | ACTIVE | `evidence/batches/P7-06C/accept.json` |
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

**P4 授权：** 用户要求完成 P4，按 P4-01～P4-05 顺序逐卡验收；不进入 P5，不启用付费模型。

**P5 授权：** 用户继续 P5，按 P5-01～P5-05 依赖顺序逐卡实施和验收；不进入 P6，不启用付费模型。

**P6 授权：** 用户开启 P6；按 P6-01～P6-05 顺序逐卡实施和验收，不进入 P7，不启用付费模型。

**P7 授权：** 用户要求继续 P7；按 P7-01～P7-05 顺序逐卡实施离线工程与实验，准备 P7-06 门禁但无显式付费授权不发请求；不进入 P8。

**P7-03 历史阻塞（已获方案 A 授权）：** 无内部 Verifier 消融与严格已验证依赖门禁存在语义冲突，真实 PG 复现保留。见 [CHANGE_REQUEST](CHANGE_REQUEST.md) 与 [ADR-0014](docs/adr/ADR-0014.md)；通过隔离基线解决，不伪造 verdict，P7-03 全部通过前不进入 P7-04/P7-05。

**P7-03 解阻授权（2026-10-25）：** 用户明确批准按 CHANGE_REQUEST 方案 A 拆分 P7-03 并增加隔离基线执行层；历史 BLOCKED 证据保留，新子批分别验收。未经额外授权不新增 DDL、不发付费请求、不进入 P8。

**后续推进授权（2026-10-25）：** 用户要求继续推进后续项目；按依赖逐卡完成 P7-04、P7-05，再准备 P7-06 真实模型评测配置。需要模型、API 密钥和费用上限时提出；未获明确配置与费用授权前不发送真实模型请求。

**P7-06 历史准备（2026-10-25，已由下列授权与实施替代）：** 96 项配置提案已生成，全部 NOT_RUN。当时 CLI 拒绝非 mock，A→B→C 拆分及模型/费用待确认。原始证据保留，不改写历史结果。

**P7-06 授权（2026-10-26）：** 用户确认 A→B→C 拆分，DeepSeek `flash-1017`，24×4 整轮 USD 2。模型不替换，96 份各 20,833 微美元，不重用/转移份额。A/B 离线验收后方可执行 C；授权已明确，当前缺密钥及官方模型/计价核实。不进入 P8。

**P7-06 当前（2026-10-26）：** A/B 已 PASS，C 的只读独立评价与整数费用对账已通过离线验证；完整保留 NOT_RUN/UNKNOWN、逐题/按类/全配对报告。真实分配 96 项仍 NOT_RUN，密钥、官方模型/单价与已应用的官方域名网络配置尚缺。环境草稿已保存，待安全填入密钥并发布；父卡和 C 保持 BLOCKED，未发送付费调用，不进入 P8。

**P7-06 最新（2026-10-26）：** 当前密钥已注入，官方模型列表 GET 200；官方版本为 DeepSeek-V4.1-Flash-1017，API 名称 `deepseek-flash`。已修正接口映射/显式 non-thinking 并冻结最高单价。实际 preflight 因整上下文预留 315802 微美元/1049600 token 超出单项 20833 微美元/65536 token 而拒绝；不是缺少密钥或重复付费授权。96 项仍 NOT_RUN，无生成请求。拟先审阅 [ADR-0017](docs/adr/ADR-0017.md)/[P7-06D](tasks/P7-06D.md) 的官方 tokenizer 输入上界方案；D 未激活，父卡/C 保持 BLOCKED，不进入 P8。前文缺配置记录为历史状态。

**输入上界修订授权（2026-10-26）：** 用户回复“先用二美元，如果实在不够了，到时候让它自动停止吧”。按前文 P7-06D 方案继续推进，整轮上限仍为 2 美元，额度不足/未知结果自动停止，不自动追加预算；保留 96 项分配和身份，不转移份额。D 先验证官方输入上界，成功后继续已授权 C；未证明上界前不发送生成请求，不进入 P8。

**P7-06D 实施检查：** 官方 ZIP 已下载并冻结；仅使用固定两条文本消息/非思考模式的官方参考模板与 ByteLevel/BPE 上界。48,000 字节 payload 对应最大 48,454 输入 token；加输出上界的最高费率预留为 15,765 微美元，已落入单份限额。正式 preflight 已 ready，但实际生成须等待 D 全量回归验收。其他未公布 API 包装不在证明范围，出现用量越界则保留 unknown 并停止。此前全上下文拒绝为历史状态。

**P7-06D 验收：** 普通 Go 全量及离线全量均退出 0；551 项 Go（含 race）、423 项 Python 和 15 个 subtest 通过，无失败/跳过/xfail。官方参考数据/闭合请求上界与 PG 预算自动停止门禁已验收；验收时正式 allocation 未使用。证据 `evidence/batches/P7-06D/accept.json`。接下来按既有授权继续 C，不增加 2 美元总额。

**P7-06C 启动：** P7-06D 已于本地提交 `be8d780` 通过，输入预留阻塞解除。先修复完整已知失败/未决工具的结果对账，再回归并执行一次原定正式分配；USD 2、Flash-1017、24×4、固定 UUID 和不重试规则保持不变。
