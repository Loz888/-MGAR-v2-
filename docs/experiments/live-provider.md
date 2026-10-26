# P7-06 · DeepSeek 受信调用边界

用户于 2026-10-26 确认 A→B→C 拆分，供应商 DeepSeek、模型 `flash-1017`，
24×4 整轮最多 USD 2。该模型标识和价格尚未从官方核实；不得自动换成别的模型。

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
runner；C 才执行真实请求。父卡在 C 验收前保持 live NOT_RUN。

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

无付费操作的检查命令：

```bash
bash scripts/eval.sh --live --preflight
```

真正执行还需 `--live --confirm-live`、`MGAR_ALLOW_PAID=1`、`MGAR_MAX_COST_USD=2.00`、
`MGAR_LIVE_CASELIST=eval/cases/frozen-live-24.json` 和安全注入的 `DEEPSEEK_API_KEY`。
没有单题覆盖、force、reset 或自动恢复参数。当前 `pricing.json` 明确 UNVERIFIED；
零占位价格不被当作免费模型。官方资料需包含请求模型、最大上下文、最保守输入价格、
输出价格与更新时间，保存到 `official-evidence.json` 后将摘要绑定到受信价格配置；
资料超过七天需重新核实。配置 preflight 不探测网络，不代替环境实际就绪检查。

规划范围公开记录为 `fixed_business_dag_template`：沿用当前执行器支持的业务 DAG
模板，模型可提交该模板及执行工具决策；不声称验证了自由生成任意 DAG 的能力。
原有严格验证、权限与 Ready 语义不放宽。正式运行前固定 dispatch 契约；仅使用
development 题的有限本地 HTTP fixture 调试，不根据 live 模型结果调 Prompt。

最终 `EXECUTED_PENDING_INDEPENDENT_EVALUATION` 也不是验收 PASS；独立数学 Oracle、
逐题/类别/配对统计、真实调用真实性与费用对账仍由 P7-06C 完成。
