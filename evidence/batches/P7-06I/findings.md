# P7-06I · 开发集复现与修复

本卡只做开发验证和下一轮审阅候选，新增付费调用0。真实C/H结果及P7-06的BLOCKED
状态保持，不能将本地HTTP闭环称为真实大模型闭环。

## 已复现的容量与错误分类

旧development-001/002完整聚合决策分别为4019/3100字节；在官方词表允许的全部切分
中取最少token，仍分别至少1472/1108，超过1024。这个算法给出词表下界，不冒充完整
BPE tokenizer；配额桩采用这一乐观下界，连乐观下界也超额才截断。最终短回执完整决策
为145个UTF8字节，在原参考ByteLevel/BPE约束下有小于1024的字节上界，不使用byte/4。
UUID/引用会改变具体下界数值，结果证明开发样本的容量问题，不断言所有历史供应商根因。

原Go模型代理将长度、无效响应、预算/输入拒绝、未知结果统一转成403，Worker误报
policy_denied。现使用闭合model-error.v1安全信封：model_output_limit、
model_invalid_response、model_input_limit、budget_exhausted、resource_exhausted、
真正policy_denied及model_usage_unknown分别报告，无自动重试、不泄露原响应或凭据。
usage缺失/越界仍unknown；显式length且usage完整才结算已知failed。恰好到输出上限但
finish=stop的有效响应仍可成功，不能仅凭token数猜测截断。

## 完整证据与拒绝边界

aggregate.merge在Go中读取原固定直接输入并产生完整MergeResult，只把摘要回执交给模型。
发布前检查当前claim、成功工具行、CAS、预算/资源无未确认，再交原Verifier检查完整
产物。Worker记录实际回执，Artifact记录完整来源，没有假装模型重复生成了全部证据。
绑定测试覆盖19个正负例，包括跨任务/owner/epoch/plan、伪造、额外字段、未调用、
未完成/未知工具和模型、CAS回执不符、重复工具、资源未知及终态拒绝。

全量验证曾发现已知伪造回执使Workflow停在interrupted。已保留完整失败日志，并改为
在工具和账本已确认时，把原模型对象包为rejected-merge-receipt.v1交原Verifier拒绝、
按failed收尾；包装不能与合法完整MergeResult相等。绑定不明时不发布完整产物。
既有端到端伪造聚合用例已重新通过，失败证据保留；没有放宽其断言。

## 历史证据与审阅

只读核对353个历史文件、109项原始导出、574条模型和333条工具行全部匹配；旧UNKNOWN
工具仍1条、原分配epoch仍2/1。用历史源码数据快照重算C/H指标，与原JSON完全相同，
没有修改旧指纹或执行历史代码。见history-audit.json。

round3-review/plan.json仅为不可执行候选：同24×4、96个新UUID，新上限USD1.65，
每份17187微美元，结转旧上界311229后累计候选1961229微美元，小于原USD2。
输入/输出上限、报价/证明及新源码摘要已关联；13个审阅正负检查通过，marker和模型行0。
当前CLI拒绝round3，实际退出2。执行器/分析器适配、最终源代码冻结与预检仍属下一卡，
没有启动新轮。原Data验证/重规划失败仍需真实评价；既有暴露题回归不代表未见数据泛化。

完整最终Go race、Python、静态检查统计及真实退出码以本卡accept.json为准。
编译/夹具错误、容量和错误分类红测试、首次全量失败均保留，只有最后完整门禁通过才PASS。
