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
