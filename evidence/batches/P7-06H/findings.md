# P7-06H · round2 真实执行结果

用户明确要求执行 G 冻结的 round2。本轮正式执行一次，实际持续1326.712秒，
DeepSeek逻辑模型flash-1017、API模型deepseek-flash，所有540次响应标识均为
deepseek-flash。96项全部尝试并完成独立评分；没有UNKNOWN或NOT_RUN，没有重放。
正式命令退出0；独立评价退出2，状态BLOCKED，原因是严格B4真实业务闭环为0。

| 组别 | 分配/评分 | 执行完成 | 失败 | blocked | Oracle通过 | 冻结最高单价估算USD |
|---|---:|---:|---:|---:|---:|---:|
| B0 | 24/24 | 15 | 3 | 6 | 15/24（62.5%） | 0.036842 |
| B1 | 24/24 | 15 | 3 | 6 | 15/24（62.5%） | 0.065843 |
| B2 | 24/24 | 15 | 3 | 6 | 21/24（87.5%） | 0.085676 |
| B4 | 24/24 | 0 | 18 | 6 | 6/24（25%） | 0.089018 |

总计45项执行完成、27项失败、24项blocked；Oracle通过57/96。
B4的6项通过全部是负例的正确拒绝，没有一项满足严格Workflow成功、Verifier产物、
独立数学/沙箱Oracle与真实调用同时成立的业务闭环。不能据此宣称真实多Agent系统验收通过。

独立评价保留全部96分母，按题、按类及24对完整配对输出JSON/CSV/Markdown。
24对Oracle差值（右减左）：B1-B0=0，B2-B1=+0.25，B4-B2=-0.625，B4-B0=-0.375。
5000次bootstrap的区间见独立报告。这里只描述这一轮既有暴露题上的修复后回归，
不合并旧轮、不声称未见数据泛化、自由DAG能力或总体协作收益。

## 预算与结束原因

新轮批准上限USD1.90，每份19791微美元，分配总额USD1.899936；未转移或重用份额。
新轮540次模型用量全部已知，估算USD0.277379，PG模型/工具预留金额为0。
旧轮已知估算USD0.013276、保守上界USD0.033850均保留。因此项目累计已知估算
USD0.290655，累计保守上界USD0.311229，低于原USD2；不是供应商发票。

全轮结束原因是all_96_assignments_attempted，没有因预算耗尽或未知结果提前停止。
正式controller状态为EXECUTED_PENDING_INDEPENDENT_EVALUATION；评分后的BLOCKED
是缺少B4严格业务闭环，与是否完成执行、是否费用超限分别记录。
全部96项原始模型行、319项工具行、预算与保留PG逐项匹配，没有未导出的本轮调用。
新分配marker为cancelled、epoch1、lease revision0、零消费；旧marker仍为epoch2。
已用轮次的只读重入预检实际退出2并拒绝，未再次发起生成。

## 可观察到的修复效果与剩余问题

- live-004四组data.query均为已知failed，未知模型/工具计数0；随后分配继续。
  旧轮同题B0的UNKNOWN原账不改写。实测证据见readonly-failure-regression.json。
- B4的18个实际Planner均完成初始计划输出，前三题的旧Planner包装错误未再成为初始
  阻塞；不据此宣称所有后续重规划输出正确。
- B4有13个aggregate/policy_denied；对应末次模型调用全部为已知failed，输出token
  均达到冻结1024上限。完整MergeResult的输出容量与通用错误归因需要在development
  复现。原响应文本/finish_reason未保留，现有证据不能确定截断的完整根因，也不能把
  通用policy_denied直接归为工具越权。逐项证据见failure-findings.json。
- 其余B4主错误包括3个Data工具故障、1个replan_model_output_invalid、1个
  verification_failed_data；预期负例另有3个insufficient_evidence、3个policy_denied。
  本卡仅记录，不修改冻结契约或重跑。

## 验证记录与局限

冻结前后source fingerprint均为G的af44b894443ce37eb184dedf205b6fea44f194e9de54805d86393f77d10331e3；
旧轮43个journal文件hash与原指标保持一致。H任务卡仅在运行结束后的验收整理阶段加入。
运行前预检、独立分析器46项测试、PG对账、只读故障回归、冻结完整性检查和重入拒绝
均有真实命令日志；最终完整工程检查为635 Go（含race）、435 Python、15子测试
和vet/Ruff/mypy/静态文档检查全部通过，退出0，无失败/跳过/xfail，详见accept.json。

首次PG对账脚本误把新分配lease revision检查为1，实际源码CreateRunClaimed明确初始
epoch1/revision0。已只修正证据脚本并重新只读对账通过，初次退出1日志保留；未修改DB。
平台配置观测仍为unknown；实际540次供应商用量已知证明的是本轮操作成功，不能把它
写成平台所有网络/凭据状态ready。模型别名可移动、控制器日志非供应商签名的限制仍适用。

执行后首次全量工程检查退出1：TestRound2PreflightRejectsChangedCheckpoint/valid
读取了正式控制库的已消费marker，因此正确收到拒绝。保留完整Go race失败日志，
只将该测试隔离到每个子测试自有的真实PG schema，并新增allocated拒绝断言。
正式marker、冻结生产代码、已执行报告和Oracle均未修改；交付指纹与执行指纹分别记录。

建议下一批在development复现聚合输出容量及model错误分类，补充符合真实输出上限的
契约测试，再单独审阅后续冻结轮；本卡不进入P8，不自动安排或执行新付费实验。
