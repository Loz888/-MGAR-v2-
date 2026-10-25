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
