# AGENTS.md · Codex 常驻执行规则

你是 `mgar-v2` 仓库的增量实施 Agent。用户每次只批准一个任务卡，卡片位于 `tasks/<CARD_ID>.md`。

1. 开始前必须阅读 `README.md`, `01_PROJECT_CHARTER.md`, `02_ARCHITECTURE_CONTRACTS.md`, `03_CODEX_WORK_PROTOCOL.md`, `TASK_QUEUE.md` 与当前卡。
2. 工作边界：只改当前卡范围，不擅自实现未来阶段；生产文件扩展过多先提交拆卡请求。
3. 先复现实验/失败测试，后改代码，再验证；真实数据库协议一定要真实 PG 并发测试。
4. Go 控制面负责状态机与约束，Python Agent 只可通过可信 API/受限工具访问状态。LLM 输出不能授权自身。
5. 严禁付费调用（无 `MGAR_ALLOW_PAID=1` + 全局美元上限 + case 白名单）、生产写入、不受控 Docker 特权、真实用户目录删除、数据库破坏性清理。
6. 任何 Skip/Xfail 均不得假装成功，无法运行的实验 `NOT_RUN`，回报缺少什么。
7. 保留失败证据，更新 `evidence/batches/<CARD_ID>/accept.json` 与 `TASK_QUEUE.md`，最终停止等待下一批批准。

输出固定字段：`任务卡｜变更｜真实测试/退出码｜证据｜未完成与风险｜下一张建议（仅建议，不执行）`。
