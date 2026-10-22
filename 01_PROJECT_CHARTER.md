# 01 · 项目宪章、功能边界和架构决策

## 1. 技术目标与非目标

**技术目标**：研究和实现多 Agent 控制面的核心系统问题：动态 DAG、增量重规划、并行资源调度、Agent 间任务与产物交接、上下文工程、Provenance 与验证、Agent 权限与模型成本治理。证明方案在指定任务集下的工程正确性与可衡量的协作收益/代价。

**非目标（防止项目无限膨胀）**：模型训练/微调；通用超大规模分布式集群；新建 Git/容器运行时；GPU 推理引擎；自由聊天式多 Agent 社交；真实生产数据库写入；真实支付与外部不可逆操作；多租户 SaaS；未经测试的跨主机断电/网络分区容错承诺。

## 2. 架构：LLM 决策、代码执行

`Task API/CLI → Go Orchestrator（Plan Validator + DAG Scheduler + Handoff + Context Policy + Budget/Router）→ PostgreSQL（事务性控制面）→ Python Agent Worker（Agent Loop + Tool Gateway）→ Artifact Store / Isolated Tools → Verifier → Aggregator`。

- LLM 可提议 DAG、任务目标、工具调用、纠错建议；**不能**直接修改 SQL 中的任意状态、授予权限、越过预算或不经校验地触发外部操作。
- Go 负责确定性状态转移、Plan CAS、交接、就绪判定、资源授权、错误分类、审计和回放。
- Python 负责受预算控制的 Agent Loop、Provider 适配与角色专用工具调用。
- PostgreSQL 存工作流与业务控制状态；Artifact 内容单独存储，数据库保存不可变元数据与引用。
- 不允许在 DB 长事务中调用 LLM、远程 API 或执行测试命令。

## 3. 角色定义（不是 Worker 数量）

- **Planner**：只提交合法结构化候选计划（节点、依赖、角色、输入依赖、验收要求、预算）；计划经 Go Validator + CAS 才能生效。
- **ResearchAgent**：文档/仓库/日志检索与事实发现，带证据引用；只读。
- **DataAgent**：合成业务库只读 SQL（解析校验、超时、行限制）和度量分析；只读。
- **ActionAgent**：受批准的测试环境代码修改/实验操作，写入隔离工作树；无生产权限。
- **Verifier**：程序化检查 Schema、来源、哈希、受控 SQL 与测试执行；可选 Reviewer LLM 用于提出补充检查，不能自行宣告确定性验证通过。
- **Aggregator**：只汇总得到有效引用和验证状态的 Artifact，显式报告未知/未验证之处。
- **Worker**：执行进程，不等于 Agent。一个 Worker 可执行多个任务实例，但每个任务实例有独立上下文、工具权限、执行身份。

## 4. 技术选型及可替换边界

- Go：控制面、事务访问（pgx/v5）、CLI、HTTP 控制 API、OpenTelemetry。
- Python 3：Agent Loop 与 Agent/Tool Adapter，typed Python、pytest、ruff（如已有版本，明确锁文件）。
- PostgreSQL 16+：事务、任务/计划/消息存储；不得仅靠内存保证正确性。
- Docker/Compose：可复现组件和沙箱；开发支持 Linux/WSL2，Windows 原生不作为 SIGKILL 验收目标。
- Model Provider 接口：`MockProvider`（完全离线）；支持一个实际 Provider（例如 DeepSeek）并保留明确价格版本、usage 精度、失败分类和预算。
- Artifact Store：起始本地私有目录内容寻址 + 原子写/校验 + 元数据；设计可替换接口，**无需**首版接入 S3。
- MCP：后期仅选择性增加工具适配；A2A：外部跨服务互操作确有需要时再做，不列为最终发布强制项。
- LangGraph/CrewAI 等仅可做评测基线，**不可**替代本项目最重要的编排实现。

## 5. 既有项目的关系

- 新旧项目独立仓库、独立 README、独立实验与指标；不得把旧项目的测试结果搬过来作为新项目成绩。
- 新项目可定义 `ExecutionAdapter` 通过进程/HTTP 调用旧 Coding Runtime 的受控接口；这是可选深度集成。默认 `LocalSandboxAdapter` 单独复现，确保 MGAR 能独立运行。
- 旧项目：一个 Coding Agent 任务内部如何可靠执行、写 Patch 和恢复。
- 新项目：多个异构 Agent 如何分工、协调、沟通、管理共享信息并在失败后维持全局语义正确。

## 6. 核心不变量

I1. 非终态工作流才能新增任务；终态不重新进入 running。
I2. 每个激活计划 DAG 有界且无环；节点依赖只引用同一工作流中的有效节点。
I3. 计划更新追加新 Plan Version，通过 `expected_plan_version` CAS 激活；旧计划及已产生证据不得篡改。
I4. 只有全部依赖在当前计划语义下满足且所需 Artifact 版本有效，任务才可进入 ready。
I5. 每个任务实例的提交必须同时检查身份/epoch/lease/revision；过期 Owner 永远不能提交结果。
I6. Handoff 事件与状态更改同一事务持久化；至少一次投递 + 幂等消费，不承诺外部 exactly-once。
I7. Artifact 不可原地改写；每个消费任务固定 `artifact_id + content_hash + plan_version` 输入快照。
I8. 未验证/无授权产物不能被标记为已验证成功；Verifier 与受审 Agent 权限分离。
I9. 全局成本/次数/并发预算在 DB 或可信控制面原子预留；未知付费调用按保守上界记录。
I10. 沙箱里的不可信代码不持有控制面/模型 API 密钥；危险工具默认拒绝。
I11. 所有产物、Trace、模型用量与结果均可回链到 Workflow/Task/Artifact/Call，敏感内容默认不进日志。
I12. Mock 工程正确性与真实模型任务正确率，分别统计和表述。

## 7. 基准故事与独立验证集

核心任务 `incident`：合成订单表、业务日志、变更说明、受控 Go 数据处理程序；故障包括重复计数、漏过滤、错误聚合和性能异常，Ground Truth 由测试断言/SQL Oracle/人工写定的需求约束构成。除修复外再配置 `analysis` 无代码变更任务，检验平台并非仅写代码。

所有环境数据都是合成的，测试覆盖：单跳、并行无依赖、Fan-in、错误证据、版本过期、工具失败、预算耗尽、崩溃接管、无解/应拒绝等情况。任何真实模型评测须使用冻结数据集 + 独立 Oracle，不在跑完后偷偷更改 Ground Truth。

## 8. 安全与信息治理

- 不保存 `.env`、API Key、数据库密码；所有连接信息走环境变量和最小权限凭据。
- LLM 输出与检索文档视为不可信输入；工具调用经过服务端 schema/权限/参数检查。
- 只读 SQL 强制独立 PostgreSQL Reader Role、只读事务、超时、行上限、表白名单；AST 检查不能代替数据库权限。
- 外部付费调用、网络接入、对宿主项目写入、不可逆删除需要显式配置且默认关闭。
- 只允许沙箱内合成资产的自动代码修改。高风险操作走显式 Approval Gate；没有审批则安全失败。
