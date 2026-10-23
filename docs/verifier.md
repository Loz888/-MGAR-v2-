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

## P5-02：固定输入的独立重执行

`Reexecutor` 只接收可信注册表里的 command_id/dataset_id，不接收任意 SQL、shell 或宿主路径。
SQL 使用独立非特权 Reader 登录、REPEATABLE READ READ ONLY 事务以及仍然存活的
exported snapshot；导入后核对整个合成输入的指纹，才运行登记的查询。缺失/过期快照、
错误输入指纹、超时、超限或特权连接都返回 unverifiable，没有退回当前数据的路径。
测试在导出快照后真实插入新行，证明 ORDER BY 与活数据读取不能替代快照约束。

SQL 行作为多重集合比较，列顺序保留，重复行不丢失。数字按精确有理数规范化，
1、1.0、1e0 等价；数字字符串仍是字符串。指数、行列数、输出大小都有上限。
声明值还必须等于实际产物里的 Observation，不能用正确的附件掩盖错误产物。
人工冻结的 incident oracle 位于 fixtures/incident/oracles，独立于 Agent 及内部 Verifier。

Go Test 注册表绑定完整源码/测试文件指纹、工具链和预期测试名。
SandboxRunner 返回由执行边界收集的真实退出码与 Go JSON events；成功还要求预期测试确实
run/pass、包完成、无 skip。单有 exit=0 不够。P5-02 仅在宿主上执行明确冻结的人工作例，
没有对任意不可信代码提供宿主执行适配器。P5-04 必须接入无凭据 Docker 并重跑本卡用例。
任意随机/时间依赖测试不保证 bitwise 重放；没有固定输入即不可验证。
