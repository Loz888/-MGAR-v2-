# P7-06C · 首轮真实模型结果（2026-10-27）

P7-06E 已通过全量验收并在原 marker 上完成一次 epoch 1→2 接管。
正式命令实际退出 0，报告状态为 STOPPED_UNKNOWN，不能将 CLI 的退出 0 当作实验 PASS。
DeepSeek `deepseek-flash` 实际返回了 34 次已知供应商用量，账本与原始报告已逐项对账。
未自动重试任何调用，未修改本轮模型、题目、Prompt、Oracle、UUID 或份额。

| 组别 | 执行完成 | 已知失败 | UNKNOWN | NOT_RUN | 已评分 | Oracle通过 |
|---|---:|---:|---:|---:|---:|---:|
| B0 | 3 | 0 | 1 | 20 | 3 | 0 |
| B1 | 3 | 0 | 0 | 21 | 3 | 0 |
| B2 | 0 | 3 | 0 | 21 | 3 | 0 |
| B4 | 0 | 3 | 0 | 21 | 3 | 0 |

已知用量按冻结最高单价估算 USD 0.013276；unknown 整项份额保留后的报告上界
USD 0.033850，均低于原整轮 USD 2。34次模型用量全部已知，未知的是只读工具结果，
不是模型账单；这些金额不是供应商发票。剩余83项未运行，配对比较和完整均值均N/A。

## 自动停止的直接原因

第13项（索引012，live-004/B0）是冻结的 `task_failure` 故障题。
`internal/demo/experiment.go` 对该类 data.query 固定返回 injectedToolFailure，
没有调用实际SQL重执行器。`internal/policy/gateway.go` 对已开始执行后返回错误的工具
统一结算为 unknown；本例留下一个 readonly data.query unknown 记录。
该项的1次模型调用已按259微美元结算，工具未知使整份20833微美元保留，runner按既有
门禁立即停止整轮。记录见012.raw/result.json及recovery-activation-and-stop.json。
本轮没有据此修改旧账或继续剩余分配。

## 真实决策质量问题

- B2与B4的前三题均未形成合法Planner输出；B4记录verification_failed_planner，
  B2记录invalid_planner_output。实际Planner输出是answer/citations等业务回答包装，
  不是控制器要求的Proposal，正确被现有Validator拒绝。
- B0/B1虽然各有3项执行完成，独立Oracle均未通过。已观察输出是investigate类回答；
  incident样本没有通过独立沙箱的修复验证，不能把工具观察或执行完成计为业务闭环。
- 尚无真实B4严格Workflow成功，无协作收益或24×4完成结论。34次真实生成只能证明
  Provider、计费与受控决策路径已运行，不能替代系统有效性验收。

## 下一批建议（本轮未实施）

在development数据上明确每个角色的任务和输出Schema，解决Planner与业务回答包装的
契约混用，补充代表真实失败形状的本地HTTP测试；保持Go验证/权限边界。
核对可证明未执行/已停止的只读故障与真正未知结果的分类，未知副作用仍不得重放。
本轮冻结holdout和旧账保持原样；修复离线验收后，另行审阅下一轮冻结分配与剩余预算，
不通过恢复开关重复已运行样本，也不自动进入P8。
