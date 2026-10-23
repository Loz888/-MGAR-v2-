# P2 Agent SDK

Version: agent-sdk.v1

P2 在 P1 持久控制面之外提供 Python 决策和角色工具适配层。SDK 不持有控制面管理员凭据，
不直接更新 Task、Workflow、计划、预算账本或独立验证结论。
`Agent.run(task, snapshot, tools, budget)` 的最终输出是未验证候选结果；
受信调用者须继续使用 Go 的 Claim/Epoch/Revision/Lease 和版本守卫才能提交 output_ready。
SDK 局部回合与工具额度不代替 PostgreSQL 的全局成本预留，SDK 事件也不代替数据库审计。

模型只能提出严格 JSON 的工具调用或最终结构化输出。工具注册、角色、参数 Schema、
资源域、最大输出与超时由受信配置决定；目标和检索文本不能扩展能力。
ContextSnapshot 遵循既有 context.v1 契约，并与任务身份和计划版本匹配。
配置、上下文、Observation 和事件保留固定副本，后续轮次不能改写已发生的历史。

默认完全离线；Mock 只替代模型决策，工具本身实际执行。Mock 结果不能证明真实模型推理能力。
SDK 的结构完成不能宣称证据独立验证成功。坏 JSON、未授权工具、安全失败或预算耗尽都必须有界结束。

本地工具处理器只允许受信实现，不能把任意代码注册成处理器并据此声称沙箱隔离。
Linux/WSL2 的主线程期限防护与各适配器的 HTTP、SQL、子进程超时共同限制执行。
不能用遗留后台线程冒充停止执行；完整不可信代码隔离由 P5 交付。
跨进程恢复和计费结算仍由 Go 持久控制面及后续对应任务负责。

P2-01 证据：`evidence/batches/P2-01/accept.json`。各后续角色和 Provider 仅在对应任务验收后列为已实现。

## P2-02 Provider 边界

结构化 Mock Provider 根据工具 Observation 决定下一轮；兼容旧脚手架的文本 Mock API
仅用于旧调用方，不能据此声称执行过 Agent Loop。
DeepSeek HTTP 适配器使用严格的结构化响应解析、有限请求/响应大小与期限，拒绝重定向。
本轮仅通过本机 HTTP stub 测试真实传输和错误分类，不发送外部模型请求。

真实请求必须同时满足付费开关、有限正数的全实验金额上限、明确 case 白名单、凭据和
受信的调用账本准入。默认没有可用的持久 Go 调用预留实现，因而真实请求保持拒绝。
测试账本只用于 stub 实验；它不是 PostgreSQL 全局费用保证。P6 接入持久预留前不能
以 Python 计数器替代 02.I 的原子预算与已提交 reserved 记录。

每个逻辑步骤有 logical_call_id，每次物理尝试有新的 call_id。
发送后的超时或断连保守进入 unknown，保留费用上界，不自动重播；未知 usage 保留 null，
不编造成零 token。429/503 只有明确的未处理证据时才允许在次数、时间和退避额度内重试。
响应结构错误、参数越权和安全拒绝不能靠无限重试“修好”。事件不保存密钥或完整 Prompt。
真实费用、模型效果与端到端付费调用没有在本轮验收。

## P2-03 来源约束检索

`ResearchAgent` 通过 `research.search` 与 `research.read` 使用合成资料索引。
`SourceIndex` 记录固定清单、来源版本、质量标签和实际内容 SHA-256；工具参数只允许来源 ID，
不接收任意宿主文件路径。相对路径逐组件打开并拒绝符号链接，文件变化后哈希失配即拒绝。
搜索 Observation 含候选来源，后续读取与最终引用由模型决策驱动。

结构化 Finding 的出处必须对应本轮实际读取的来源、内容哈希及原文 quote。
不存在的来源、未读取的出处、改写哈希、虚构 quote 或自行声明 verified 都会被拒绝。
文本线索仍可能语义错误，引用一致性不等于独立验证。低质量资料保持未验证。
`artifact://` 此时表示带 Producer 身份的不可变候选引用；P3 内容存储和 P5 验证尚未接入，
不能把候选引用宣称为已经持久化且可信的 Artifact。

## P2-04 只读数据工具

`DataAgent` 的受信 SQL 工具使用独立 PostgreSQL Reader 凭据，只能访问获准的合成订单表。
控制面管理员凭据只用于测试基础设施创建其拥有的随机测试 schema/role，不交给 Agent 或模型。
Reader 不继承控制面读写角色；SQL 解析不能替代数据库 ACL。

SQL 工具执行单条受限 SELECT：维护的 SQLGlot AST 白名单校验表、表达式和聚合函数，
拒绝 DML/DDL、注释绕过、多 statement、危险函数、越权表、锁和其他未支持语法。
参数通过数据库绑定；查询在 REPEATABLE READ READ ONLY 事务执行，限制连接、语句、锁等待、
返回行数和输出字节。只读事务与 Reader ACL 分别验证，不依赖正则表达式或 Prompt 保证只读。
连接只允许本机 55432 的 mgar_test 合成数据库命名空间；显式 libpq 目标参数防止环境覆盖路由。

Observation 记录实际 SQL、参数、数据库快照、fixture 版本、列类型、返回行数、结果与摘要。
最终数值引用必须对应本轮实际查询得到的单元格及类型；模型自行填写的数字不能替代查询结果。
金额等精确数值保留数据库精度，不因 JSON 浮点转换伪造精度。
数据证据依然是未验证候选；真实生产库、独立 Verifier 和持久 Artifact CAS 不在本卡范围。
