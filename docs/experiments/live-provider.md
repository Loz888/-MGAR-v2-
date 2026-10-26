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
