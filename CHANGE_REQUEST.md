# CHANGE_REQUEST · P7-03 基线验证语义

状态：**用户已批准方案 A（2026-10-25）：P7-03A 已验收（6cfb0bf），P7-03B 已完成全部 60×7 离线运行并通过验收。证据见 `evidence/batches/P7-03B/accept.json`。**

P7-01 已完成（`15f214b`），P7-02 已完成（`dc3411d`）。
P7-03 的无内部 Verifier 组无法直接复用现有严格依赖链：真实 PostgreSQL 复现中，
没有 verdict 的 source 停在 output_ready，sink 不能 Claim，Workflow 不能成功。

依据与完整选项：[ADR-0014](docs/adr/ADR-0014.md)。
复现：[边界测试](tests/integration/eval_boundary_test.go)，
[真实日志](evidence/batches/P7-03/conflict-reproduction.log)。

## 已批准的具体范围（方案 A）

将 P7-03 拆成两个依赖顺序执行的子批：

- **P7-03A**：冻结并实现隔离基线的候选证据交接/结果语义。复用相同合成工具、模型版本、
  预算与权限约束；候选明确未语义验证，实验完成不冒充严格 Workflow succeeded。
  先验收跨 Run/Role、哈希/版本变化、预算拒绝、失败不丢弃；保持现有生产门禁。
- **P7-03B**：在已验收适配上完成七组公平 runner，逐组独立 namespace，同题同上限，
  冻结外部 Oracle 统一评分，记录全部失败和实际开销，再进入 P7-04/P7-05。

不授权新增 DDL、付费调用或进入 P8；如果子批实际需要迁移，再提交具体迁移申请。
七组主比较指标一致；严格 Workflow 成功诊断对独立基线标记 N/A，单独解释。

替代方案 B 是修改核心验证等级与 Ready/Context/Merge 契约，影响更大；若选 B，
先另行冻结变更设计与迁移范围，不直接改代码。

历史阻塞记录保留。批准后按 P7-03A → P7-03B 顺序实施验收；runner 完成前不宣称 P7-03 或 P7 全部完成。
暂停依据：`03_CODEX_WORK_PROTOCOL.md` 的“需求与协议冲突：先写 ... ADR ... 卡状态
BLOCKED，等待用户批准”，以及用户要求出现规格冲突时提交 ADR/CHANGE_REQUEST 并暂停。

## P7-06 · 真实 Provider 接通与有限预算执行（已批准）

2026-10-26 用户确认拆分，DeepSeek `flash-1017`、24×4 整轮 USD 2；依次实施 A/B/C，
不进入 P8。以下为原提案记录，原先缺授权条件已解除；密钥、官方价格/标识与网络仍需落实。

P7-04/P7-05 已通过。P7-06 目前 **BLOCKED / live NOT_RUN**：现有 Python Provider
尚未接入 Go 实验预算与执行层，不能只填 API key 就视为接入完成。实际 CLI 拒绝
非 mock 的退出码 2 已记录，详见 [ADR-0016](docs/adr/ADR-0016.md)。

建议按三个可验收子批拆分：

1. [P7-06A](tasks/P7-06A.md)：可信真实 Provider 与 durable 账本接通；本地故障服务器验收。
2. [P7-06B](tasks/P7-06B.md)：接入四组 live runner、96 个冻结分配与总预算门禁；离线验收。
3. [P7-06C](tasks/P7-06C.md)：用户明确模型/密钥/价格/费用后执行真实 24×4 和对账。

原提案候选为 DeepSeek Chat、预算留空；现已由用户指定 `flash-1017`、整轮 USD 2。
官方单价仍待核实，密钥仅通过环境 Secrets 配置。
[实验配置提案](eval/live/plan.json) 列明全部 96 项，均为 NOT_RUN。A/B 的离线接入
提案原先未包含 C 的付费授权；用户本次已给出上述具体授权。不新增 DDL、不进入 P8。
原暂停依据为 `03_CODEX_WORK_PROTOCOL.md` 第 4 条；本次批准已解除拆卡暂停。

## P7-06 输入预算上界修订（2026-10-26，待批准）

A/B 已验收，当前密钥已注入、官方模型列表请求 200，官方版本映射与最高费率已核实。
无需再次配置密钥或重复批准整轮 2 美元。接口映射与 non-thinking 参数已修正并测试。

新增实际阻塞：[ADR-0017](docs/adr/ADR-0017.md) 复现全上下文预留为
1,049,600 token / 315,802 微美元，超出每项冻结的 65,536 token / 20,833 微美元。
这不是实际费用估计。正式 preflight 在创建分配/发生成请求前拒绝；96 项仍 NOT_RUN。

建议批准 [P7-06D](tasks/P7-06D.md)：先用官方 tokenizer 和聊天包装契约证明实际请求
的输入上界，再复用原有 PG 预算与 unknown 保护。全部 USD 2、题目、组别、分配 UUID、
工具权限和 Verifier 门禁保持不变；不新增迁移，不进入 P8。无法证明就继续阻塞。
该子卡当前仅提案，未实施。批准后 D 离线验收，再回到原 C 执行。

暂停来自 `03_CODEX_WORK_PROTOCOL.md` 的“需求与协议冲突 ... 卡状态 BLOCKED，等待用户批准”；
具体需要审阅的是输入预留算法变化，既有 A/B/C 与费用授权继续有效。
