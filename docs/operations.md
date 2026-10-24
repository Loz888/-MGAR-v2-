# 本地运维 CLI（P6-05）

先加载本仓库 `scripts/dev-env.sh`，使用 Go 1.27.3 构建 `go build -o .local/mgarctl ./cmd/mgarctl`。数据库仍限制为 loopback:55432 的 synthetic `mgar_test` namespace，凭据只从本机配置读取。设置 `MGAR_ARTIFACT_ROOT` 指向已经存在、0700 权限且无符号链接的私有 CAS 目录；诊断不会创建它。

| 命令 | 行为 |
| --- | --- |
| `mgarctl status RUN_ID` | 同一只读快照中的 Run 状态、所有计划版本、Task attempts/retries/epoch、产物与输入版本、工具状态、预算、资源压力。 |
| `mgarctl trace RUN_ID --format json` | status 数据加事件/模型成本/交接/Verifier 审计时间线。Span 缺失单独标记；不发送 OTLP。 |
| `mgarctl budget-report RUN_ID` | 上限、已结算/预留/unknown 用量和与独立账本比较的 conserved；不会纠正漂移。 |
| `mgarctl inspect-artifact ARTIFACT_ID [--run RUN_ID]` | ACL 后查元数据，读取实际 CAS 字节校验长度与 SHA-256。可选 run 限制产物归属；不显示正文或执行内容。 |
| `mgarctl doctor [--format json]` | 检查配置、DB 权限、六个迁移的校验和、unknown/超期 held 执行、CAS 目录、Go/Python/本机 Docker。无修复/清理动作。 |
| `mgarctl cancel RUN_ID` | 显式取消，沿用状态机；未确认的副作用账本仍保持保守记录。 |
| `mgarctl resume RUN_ID` | 显式恢复；终态、未知调用或不安全重放被拒绝。不会因 doctor 建议自动执行。 |

诊断默认 JSON，可显式加 `--format json` 或 `--json`。成功退出 0；运行失败/doctor degraded 退出 1；参数错误退出 2。诊断错误使用 `cli_error.v1` 固定错误码，不打印 DB 原始异常、连接串或错误行。doctor 返回逐项结果和始终为空的 `actions_taken`；它不会调用 Migrate、删除 schema、CAS 或容器，工具探测有两秒期限与输出上限。

本地 DB session/current role 是身份来源：`mgar_reader` 属于可信运维角色，可读取全部 synthetic runs；`mgar_worker` 无控制表读权限。不存在 `--identity admin` 或模型自报身份路径。这不是面向不可信多租户用户的认证接口。单 Run 查询不返回其他 Run 的实体；资源 `global_occupied` 是共享池总体占用数量，不含其他 Run ID。产物不指定 `--run` 时由已获全局读取权限的本地操作者按 UUID 定位所属 Run；附加 `--run` 可防误选。

`trace` 中 proposer/owner/capability 使用 SHA-256 摘要，不输出任意原文标识；相同操作者摘要可串联计划与接管事件。Task 的 `Attempts` 是领取次数，`Retries` 是已有状态机重试计数，不能混为一谈。数据从同一 repeatable-read/read-only 事务读取；每类列表超过 4096 项会明确 `truncated=true`。`bytes_verified=true` 只表示 CAS 内容哈希/长度正确，业务结论仍以 `latest_verdict` 和独立 Verifier 记录为准。

遇到 unknown 或超期 held 时，先独立确认执行是否已停止，再使用已有可信控制面恢复 API；不要仅凭 TTL 清空许可或把 unknown 改成成功。`budget.conserved=false` 是账本差异，需要调查；CLI 不自动修复。缺少 CAS、运行依赖或迁移校验不符时，doctor 报告失败而保留现状。

本轮已运行真实 CLI 子进程、真实 PostgreSQL 权限/只读快照和实际 CAS 检查；没有付费模型、生产数据库写入或部署外部 collector。完整 Worker/系统场景组装仍由 P7 后续任务负责。
