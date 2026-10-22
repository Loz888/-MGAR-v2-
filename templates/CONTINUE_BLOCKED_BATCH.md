# 继续当前未完成任务（不要跳卡）

上次任务 `<CARD_ID>` 未完整 PASS。请读取 `evidence/batches/<CARD_ID>/accept.json`、相关失败日志、Git diff 与任务卡，**只针对该卡**处理阻塞。

先重现之前的失败，记录根因和最小修复；不得为了测试通过跳过用例、修改 oracle、删除失败日志或降低约束。修改后跑卡内测试和已存在回归，再更新证据与状态。若不可在现有边界修复，提交 ADR/CHANGE_REQUEST 并停止。
