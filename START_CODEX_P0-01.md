# 第一批 · 给 Codex 的启动指令（直接复制）

请在一个**新的独立 Git 仓库**中实施 MGAR v2 项目。你必须先阅读根目录 `AGENTS.md`、`README.md`、`01_PROJECT_CHARTER.md`、`02_ARCHITECTURE_CONTRACTS.md`、`03_CODEX_WORK_PROTOCOL.md`、`07_IMPLEMENTATION_PLAYBOOK.md`、`TASK_QUEUE.md`，再阅读 `tasks/P0-01.md`。

**本轮唯一任务：P0-01（冻结边界、ADR 与契约索引）。** 不得越过该任务去实现 P0-02、P1 甚至整套系统。

1. 先检查 git status 和当前目录，不能覆盖我已有的修改。
2. 用任务书中的实体/状态机/信任边界建立架构文档、ADR 与静态规范检查，目标是让后续每批有统一可依赖的设计契约。
3. 按本卡编写并运行验证命令，记录真实退出码和失败/通过日志；不能声明未运行命令成功。
4. 更新 `TASK_QUEUE.md` 和 `evidence/batches/P0-01/accept.json`。如果规格出现冲突，提交 ADR/CHANGE_REQUEST 并暂停，不能猜测性变更。
5. 最终只给出本卡的变更、测试、证据、风险、后续建议，然后**停止并等待我的下一批指令**。

严禁：默认付费模型调用、写真实业务数据库、修改现有 durable-coding-agent-runtime-source 仓库、跳过测试、扩大本轮任务范围。
