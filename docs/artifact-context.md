# Artifact 与 Context 实现

Version: artifact-context.v1

P3-01 提供真实本地 CAS 与受控 `PublishArtifact`。相同字节只存一份，
每个生产任务仍有独立 Artifact UUID、Run、Task、计划版本、内容类型和工具版本。
外部引用为 `artifact://<uuid>`；磁盘位置仅由受信服务按哈希计算，不能用 URI 作为宿主路径。
读取必须重校验 SHA-256、大小、文件类型和链接数。

发布时把元数据、Task output_ready、运行槽释放与事件/Outbox 放入同一 PostgreSQL 事务，
保持原有 Claim 守卫；不会把内容标成独立验证通过，也不会完成整个 Workflow。
追加迁移 002 保留发布迁移 001 不变；旧测试元数据保留 legacy_fixture.v1 标签，
不能以这些历史合成行证明文件已经落盘。

目录仅允许可信服务使用；提供物理内容 Inventory，禁止自动破坏性 GC。
数据库失败或提交结果未知时不得删除已写内容，协议见 [ADR-0003](adr/ADR-0003.md)。
当前尚未接通 Python Worker 自动派发与结果回写；P2 角色结果仍是候选，
只有受信控制面显式调用发布 API 后才是已落盘内容。独立 Verifier 仍属于 P5。

## P3-02 固定输入与读取授权

`BuildSnapshot` 从受信数据库的当前直接依赖构造引用，拒绝 Worker 自选任意产物。
引用绑定 Run/Task/Role/Plan、Producer、来源版本、内容哈希、工具版本、大小与类型；
模型和工具策略配置版本也固定在快照中。追加迁移 003 保存不可变规范 JSON 及其 SHA-256，
同任务重放返回相同 snapshot_id 与字节；改变配置、来源或已撤销验证结论时拒绝，不悄然重建。
Go 与 Python SDK 的实际序列化/哈希互通经过子进程验证。

`ReadInput` 同时检查当前 Claim、固定快照归属和直接依赖授权，再校验 CAS 内容；
同内容哈希不能授予别的 Run 或私有产物访问权。内容在传给 Provider 之前仍是不可信数据。
新的 CAS 发布必须已有固定快照，并在同一发布事务中写 artifact_contexts，建立输出到输入的来源链。
执行入口须先构造快照再调用模型；尚无自动 Go→Python Worker 调度服务。
遗留 P1 协议测试可以显式预置合成元数据，不能把这些 fixture 当成真实发布结果。

## P3-03 上下文选择与实际计量

`PrepareContext` 先经过固定快照授权和 CAS 重校验，再运行确定性选择器。
full 保留授权集合的完整内容并按上限记录截断；summary 生成最多 256 UTF-8 字节的规则摘录；
selected 按角色、证据种类和目标实体匹配评分，稳定按 ID 打破平分，并记录纳入/排除原因。
这里的“全部历史”以当前快照有权访问的集合为界，不能扩展到整个 Run 的私有资料。
关键证据无法放入预算时阻止请求；低相关日志可以省略，固定输入来源清单不会被改写。

预算统计实际送给 Provider 的 snapshot_json，包括来源元数据及转义开销，
不包含目标、工具声明、后续 Observation 或 HTTP 外壳。Token 采用显式命名的
utf8_bytes_div4.v1 估算，不等于真实分词器、usage 或美元计费；这些仍由 P6/真实实验处理。
Python SDK 校验所选内容与原快照、引用哈希和原文字节一致；不允许摘要冒充原文或声明 verified。

真实 PrepareContext 的 summary 模式把有损摘录作为 kind=summary 的派生 Artifact 写入 CAS 和不可变元数据，
保留原 Artifact ID/hash、快照 hash、lossy=true、verified=false，且不改变 Task 成功状态。
同源摘要有确定的 UUIDv8，重复/并发准备只产生一份元数据和事件；原始内容不被摘要替换。
离线 contextselect CLI 只用于可信评测 fixture，不提供身份认证或数据库授权。
Mock Provider 收到的实际字节在 eval/context_selection 测试与报告中核对，不能据此宣称真实模型效果。

## P3-04 来源追踪与增量复用

`ProposeIncremental` 根据固定输入来源图和实际 Artifact/CAS 生成确定的 reuse/recompute 原因。
需求、边、模型配置、策略或工具版本变化会使本节点及其后继重算；未变化分支只有在来源、
权限域、最新 verdict 和字节完整性全部通过时复用。相同内容的不同 Producer 不会被合并。
新计划用显式 reuse_artifact_id 指向旧终态任务和旧产物，其他节点创建新 Task；
快照保留真实 source_plan_version，历史任务和文件不会因新计划或失败重算被改写。

计划、映射、配置与通知通过数据库 CAS 原子提交，并发重规划只允许一个胜者。
新增任务的模型/工具配置也在执行入口检查，拒绝执行时临时换配置。
当前函数是确定性的受信计划提交接口，不是 P4 自动 Replanner；活动执行尚未完成时拒绝增量提交。
细节与收尾/撤销语义见 [ADR-0004](adr/ADR-0004.md)。
