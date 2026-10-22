# 阶段级人工验收提示词

请只审核阶段 `<P#>` 及其任务卡的交付质量，不新增功能。

检查：1) 每卡 accept.json 与真实 logs/commit 对齐；2) 本阶段全部正负例、race、PG 并发、安全/杀进程测试按阶段要求实际运行；3) 历史失败记录未删；4) 核心协议/文档一致；5) 所有 NOT_RUN 仍标明缺条件；6) 任务状态与 Git 工作树一致。

输出 `evidence/batches/<P#>/stage-summary.json`，给 PASS / PARTIAL / BLOCKED 与理由；如果有问题列出返回哪张任务卡修复。**不得自行开启下一阶段。**
