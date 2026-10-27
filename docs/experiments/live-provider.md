# P7-06 · DeepSeek 受信调用边界

用户于 2026-10-26 确认 A→B→C 拆分，供应商 DeepSeek、模型 `flash-1017`，
24×4 整轮最多 USD 2。已核实官方 API 名称为 `deepseek-flash`，当前服务版本为
DeepSeek-V4.1-Flash-1017。账本/分配保留原逻辑版本 `flash-1017`；官方别名可移动，
不声称锁定不可变权重。请求显式 non-thinking、temperature=0，四组一致。

P7-06A 在 Go broker 中接通受信传输与既有 PostgreSQL `model_calls`、资源和预算
守卫。Python Worker 仍只有本地 Gateway token；子进程环境采用显式白名单，不包含
模型密钥、DB 密码或网络代理。生产传输只接受官方 HTTPS endpoint，继承平台代理与
CA，拒绝重定向。公开调用入口必须成功完成 DB 预留才能发送，不新增 DDL。

每个逻辑调用只有一次物理 HTTP 尝试，25 秒上限。429/503、中断、无效/缺失 usage
全部保留 unknown 上界，无自动重发。usage 完整但输出不可解析则按 failed 结算已知
用量。错误不包含原始响应、认证头或 key。账本保留精确请求体 SHA-256、冻结单价、
响应 SHA-256、响应模型标识、真实来源与 token；价格估算不等于供应商账单。

在尚未验证 tokenizer 上界前，输入预留采用**官方模型完整 context ceiling**，输出
另行预留 `max_tokens`，不使用 byte/4 计费。该保守策略可能让 USD 2 / 96 的份额无法
容纳一次请求；遇到这种情况应拒绝发送并保留 NOT_RUN，不缩小未知上界以通过门禁。
后续若要用更紧的上界，须有可核验 tokenizer/服务端截断保证及测试。

`provider.NewLoopback` 仅接受字面量 127.0.0.1 的本地端口，固定虚拟凭据，永远标记
`local_stub`。离线测试使用 synthetic 单价，不将它保存为官方报价。公开真实构造器
还要求显式确认、paid flag、精确 USD 2、冻结 case 白名单、密钥和官方资料摘要。
资料摘要只提供来源关联，官方真实性必须由 P7-06B/C 的受信 preflight 核实。

本地 HTTP、真实 PG、独立 Python Worker、独立 Verifier 的开发题闭环只证明接通；
不证明模型质量、真实供应商可达性或真实模型闭环。B 负责不可复用的全实验份额与
runner；C 才执行真实请求。真实状态以正式日志和各卡 `accept.json` 为准；不能将 C 执行前的
离线验收算作真实模型实验。

## P7-06B 执行与持久化

`eval/live/plan.json` 冻结 96 项，每项固定 case/arm/UUID 和 20,833 微美元；四组使用
相同模型、温度、上下文上限与工具预算。B3/A1/A2 明确不运行。旧提案保留在
`evidence/eval/live/assignment-proposal.json`，旧 Mock 报告不改写。

先使用现有 synthetic PG 的 `workflow_runs` UUID 唯一约束建立一个零消费、终态
cancelled 的控制 Run，作为整轮分配已使用的标记。它不承载业务任务，不计为实验样本。
再独占创建固定实验目录；manifest、每项 started/raw/result 和最终报告都以 exclusive
create + fsync 保存。任一步结果不明即拒绝自动重启；不删除标记、不转移余额、不生成
新身份。已有分配被删除（甚至整个本地目录丢失）也不能绕过 PG 标记。未同时验证两个
独立数据库的部署，因此不能声称跨数据库共享额度；更换 DB 需要先人工对账。

真实分配的 PG schema 与 CAS 在结束后保留，Reader 临时角色撤销并关闭；离线测试
只清理其自有随机 schema/目录。每个实际 Run 的既有 PG 预算守卫仍逐次原子预留。
unknown 或缺失对账停止整轮后续发送；报告保留全部 96 项，未开始项仍是 NOT_RUN。
模型/工具只要仍有 pending 或 unknown，均保留原份额上界并停止。控制或清理返回错误
不抹去完整账本中已结算的费用；完整已知的失败仍记 failed，不能改成成功或重跑该分配。

无付费操作的检查命令：

```bash
bash scripts/eval.sh --live --preflight
```

真正执行还需 `--live --confirm-live`、`MGAR_ALLOW_PAID=1`、`MGAR_MAX_COST_USD=2.00`、
`MGAR_LIVE_CASELIST=eval/cases/frozen-live-24.json` 和安全注入的 `DEEPSEEK_API_KEY`。
没有单题覆盖、force、reset 或自动恢复参数。当前 `pricing.json` 已冻结官方最高费率；
输入取 peak cache-miss USD 0.30/1M、输出 USD 1.20/1M，低价时段/缓存命中也按该
保守单价估算，不称为实际账单。官方资料包含请求模型、最大上下文、最保守输入价格、
输出价格与更新时间，保存到 `official-evidence.json` 后将摘要绑定到受信价格配置；
资料超过七天需重新核实。配置 preflight 不探测网络，不代替环境实际就绪检查。

规划范围公开记录为 `fixed_business_dag_template`：沿用当前执行器支持的业务 DAG
模板，模型可提交该模板及执行工具决策；不声称验证了自由生成任意 DAG 的能力。
原有严格验证、权限与 Ready 语义不放宽。正式运行前固定 dispatch 契约；仅使用
development 题的有限本地 HTTP fixture 调试，不根据 live 模型结果调 Prompt。

最终 `EXECUTED_PENDING_INDEPENDENT_EVALUATION` 也不是验收 PASS；独立数学 Oracle、
逐题/类别/配对统计、真实调用真实性与费用对账仍由 P7-06C 完成。

## P7-06C 独立评价入口

```bash
source scripts/dev-env.sh
python eval/live/analyze.py --output evidence/eval/live/readiness-20261026
```

默认只读固定实验目录，输出到新的独立目录。示例输出已归档，重跑时使用新的输出名。
退出码 0 表示全部 96 项完成评价且至少一条严格真实调用闭环的证据满足检查；2 表示
BLOCKED（包括未执行/未知/缺少闭环）；1 表示证据损坏或输入无效。这不是发请求入口。
示例与最新 `runtime-check-20261026` 检查均实际返回 2，全部 96 项 NOT_RUN，
配对差值和整体费用估计均为 N/A。

评价器验证冻结数据、分配身份、原始报告 hash、运行时分组契约和预算；按冻结整数
微美元单价重新计算每次 usage，匹配 PG 导出的模型调用数、token、费用及未知预留。
明确拒绝 local_stub、跨 Run、重复调用、账本/汇总矛盾。已知失败调用照常计费；崩溃
丢失明细或 unknown 保留整份 20,833 微美元。NOT_RUN 不作为观察到的零费用样本。

数学 Oracle 从冻结输入重新计算，模型自评无效；incident 正向题还须有独立沙箱
执行结果。只有 B4 的严格 Workflow succeeded、Verifier 产物身份/hash/通过检查、
独立 Oracle 及至少一个已知真实来源调用同时满足，才记录真实模型闭环。该工具不
重新调用模型、不写 DB，也不在分析阶段再次执行沙箱；重执行与 SQL 证据由受信
执行器在正式运行时捕获。hash 关联假设控制器日志可信，不等于供应商签名或发票。

保留 96 项完整分母，按四组、类别输出 JSON/CSV/Markdown。配对分析只在两组全部
24 对都有相应度量时计算右减左与 5000 次 bootstrap CI；缺失保持 N/A，不取交集。
已观察通过率、全分配通过比例、NOT_RUN/UNKNOWN 数量分开。B3/A1/A2 单列未运行。
单次顺序运行与固定 DAG 模板的限制仍适用，不预设收益。

环境已注入 `DEEPSEEK_API_KEY`（仅允许发往 api.deepseek.com）；官方 API/文档的
实际 HTTPS 请求成功。平台状态仍为 unknown，仅报告实际操作结果。历史检查中的阻塞从
缺 key/报价转为输入预留：1M 上下文容量加 1024 输出，按原规则需 315802 微美元、
1049600 token，超过每份 20833 微美元与 65536 token。即使十进制 1M 也无法容纳。
这不是实际费用估计。正式 preflight 退出 2，不创建分配，不发生成请求。

上述为全上下文模式的历史拒绝。用户随后授权以 2 美元继续，不足自动停止。
输入上界修订见 [ADR-0017](../adr/ADR-0017.md)/[P7-06D](../../tasks/P7-06D.md)。
D 已完成离线验收，证据见 `evidence/batches/P7-06D/accept.json`；正式 C 结果另行记录。

## P7-06D 文本输入上界

当前模式使用官方发布且 hash 固定的 ByteLevel/BPE 数据和 chat_template。identity
normalizer、无前导空格的 ByteLevel、完整 256 字节字母表、只合并不扩展的 BPE，给出
参考输入序列 token 数不超过其 UTF-8 字节数的上界；BOS/角色 marker 按完整字节计入。
不是字节数除四或经验估算。两条文本消息的适用分支由闭合请求 Schema 强制。

为避免未证明的服务端附加包装，不启用 native JSON response_format、native tools、
图片、name、prefix completion 或 thinking。system 仍要求同一严格 JSON 决策，Go/Python
控制器继续验证实际结构、权限和证据。JSON 输出失败不自动重试或判为成功。

模型容量字段仍为 1M；payload 上限单独为 48,000 字节，输入最坏预留 48,454 token，
输出最多 1,024 token，最高报价下每次最坏预留 15,765 微美元，低于单项 20,833。
实际发送前按同一不可变请求计算并入账；16 位证明指纹后缀绑定报价版本，完整证明
和数据 hash 保存在配置与官方证据中，request SHA256 对应最终发送字节。

报价/模式/数据不符、超大请求和剩余额度不足均为零发送；实际 usage 超出所用上界，
保留 unknown，不重发，并停止后续实验分配。总上限、96 项身份、每份额度、token
预算及未知保护不变；无新增 DDL。已授权并发请求仍按各自已预留上界结算。

仅在已发布的该参考文本串行化/codec 契约内使用该证明，不覆盖供应商未公布的额外
处理或可变模型权重。数据获取工具 `python eval/live/tokenizer_data.py --output .local/<新目录>`
只下载审计，不启用模型；不执行包中 Python 代码。下载不成功退出 2。包审计成功
不等于上界证明/任务验收成功。各项独立证据在 P7-06D 验收目录。

## 初始化失败与恢复边界

本轮正式启动在生成前因实际控制库缺既有005/006迁移而退出1；全部96项仍NOT_RUN，
独立分析退出2。旧schema曾使marker写入后才失败，现增加写入前的只读迁移/结构门禁，
测试使用真实PG旧schema复现。随后补应用既有005/006，未删除或恢复原queued marker。
零发送初始化恢复例外见 [ADR-0018](../adr/ADR-0018.md)/[P7-06E](../../tasks/P7-06E.md)，
已按用户确认批准；原USD2和模型授权不变。实际诊断/日志/计数见本卡accept和正式分析报告。


## P7-06E 一次性零发送恢复

`--recover-zero-send` 只绑定本次原失败：固定证据hash、原marker与lease过期时间、
完整可读的namespace/账本零使用证明，且没有journal/CAS执行历史。只允许epoch1→2，
epoch2后即使进程SIGKILL或lease过期也拒绝再次接管。普通live启动仍按原一次性规则。

先按原paid环境门禁运行 `bash scripts/eval.sh --live --confirm-live --recover-zero-send --preflight`
做只读检查；E验收通过后，同一命令省略 `--preflight` 才完成原初始化并执行原96项。
不是用于恢复任何已发送/未知调用的开关；不删除标记、不改身份或费用，不调holdout。
执行器元数据保留allocation_mode和recovery_proof_sha256，独立评价继续使用相同Oracle。

## 开发集修复与修复后回归轮

P7-06F 将角色契约分为Planner Proposal、真实工具Observation、严格MergeResult、
基线业务报告；只对有可信停止证明且资源已释放的只读错误结算failed。其他未知保护
保持，旧轮unknown/报告不改写。开发集本地HTTP证明协议和控制流程，不代替模型质量。

P7-06G 冻结 `eval/live/round2/plan.json`：同24题×四组、96个新UUID及独立PG/journal。
实验为post_fix_regression，已有暴露题目，不是新未见holdout；不合并两轮样本。
新轮批准上限USD1.90，每份19791微美元（合计1.899936）；前轮上界0.033850保留，
累计批准上界1.933850小于原USD2。前轮report和新契约/网关源码hash绑定到配置，变更
或缺失拒绝。原数据/Oracle、模型/温度、工具能力、Verifier与token上限均不变。

加载本仓库dev-env并保留已注入密钥后，可用以下命令只读预检：

```bash
MGAR_ALLOW_PAID=1 MGAR_MAX_COST_USD=2.00 MGAR_LIVE_CASELIST=eval/cases/frozen-live-24.json bash scripts/eval.sh --live --confirm-live --round round2 --preflight
```

真正执行使用同一命令省略 `--preflight`；本批仅完成准备，不默认执行新付费轮。
选择器只支持original/round2，不能把旧恢复开关用于新轮。新轮仍用独占文件与固定
零消费PG标记、原子模型预留；未知停止，不提供force或自动重播。默认仍选择original。

独立只读评价为 `python eval/live/analyze.py --round round2 --output evidence/eval/live/<新的输出目录>`。
未运行会输出完整96项NOT_RUN并退出2，显示已知用量N/A和保留的前轮上界；任何新调用
按本轮份额重新对账，累计上界加回前轮保留额。B3/A1/A2仍未运行，不声称策略收益。
