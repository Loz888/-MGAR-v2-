# 独立证据验证

`internal/verifier.Engine.Check` 严格解码 evidence.v1，拒绝重复键、额外字段、缺少身份、
无限深 JSON、超限数据和未知协议。输出 verdict.v1：passed、failed 或 unverifiable，附逐项原因。
实际生产者、版本、哈希、工具版本和 ContextSnapshot 必须一致；引用须来自获授权的快照。
`claim` 是说明文字，不能授予权限，也不能把未检查的推论变成事实。

可信控制器的 `Requirement` 定义要证明的性质；Agent 的 expected_observation 不能改写它。
文件 SHA/引用必须匹配真实 CAS 字节；这只证明相应字节属性。
SQL/测试需要独立重执行，未配置 Replayer 时明确 unverifiable。
Reviewer 文本不属于 Evidence 的可接受字段，不能覆盖确定性失败。

`Store.VerifyArtifact` 要求可信编排租约。它在运行校验前授权引用，在独立执行后再次核对，
最后原子追加 artifact_verdicts、workflow_events 和 Outbox。检查与远程执行不占用数据库事务。
旧 verdict 不允许更新/删除；不改写原 Artifact。事件保存检查原因和输入/Requirement 摘要，
不保存完整自述或敏感源码。调度器只接受当前有效来源的最新通过 verdict。
unverifiable 也不能解锁成功路径；现有布尔门禁将其作为未通过，详细三态保存在事件中。

运行：`go test ./internal/verifier/... ./tests/integration/... -run 'Verifier|Evidence'`。
这些测试包含真实 PostgreSQL、跨进程追加和不同数据库登录凭据的负例。
P5-01 尚不把 Python Agent 输出自动转换成受信 Evidence，也没有开放 Worker 验证写接口；
SQL/Go Test 重执行与隔离分别由 P5-02/P5-04 补足。
