# 本地开发与验收

Version: development.v1

已有独立检出目录即可工作；云任务已隔离，不需要另外创建 Git worktree。
Go 工具链固定 1.27.3（Linux amd64 官方包 SHA-256
`65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40`），
Python 固定 3.12，依赖由 `uv.lock` 锁定，PostgreSQL 镜像摘要见 `deploy/compose.yaml`。

当前验收平台为 Linux 或 WSL2 的单机环境，PostgreSQL 与控制面均在本机。
首次准备运行 `scripts/setup.sh` 安装固定工具链与锁定依赖，生成本地合成数据库凭据；
再运行 `scripts/bootstrap.sh --fresh-test-db` 新建专用测试数据库并迁移。
Bootstrap 每次生成新的 `mgar_test_<随机后缀>` 数据库，保留旧数据库，不用于清理其他数据。
随后在当前 shell 中 `source scripts/dev-env.sh`，再构建。
标准系统若已有相同 Go 版本，可直接使用本机 Go；本环境系统的 `/usr/bin/go` 不是 Go 编译器。
`scripts/dev-env.sh` 激活工作区工具链、虚拟环境与缓存，并读取未跟踪的 `.local/db.env`。
只读主目录下不能使用默认 uv 缓存；不要关闭 TLS、包校验或 Go checksum database。

本地测试凭据必须通过安全随机数生成到 `.local/db.env`（0600），包括
`MGAR_TEST_DB_PASSWORD` 与 `MGAR_TEST_DATABASE_URL`。
连接仅允许回环主机 `127.0.0.1` / `localhost`、精确端口 `55432`，数据库名必须是
`mgar_test` 或 `mgar_test_` 后接 16 位小写十六进制字符；不允许任意同前缀数据库。
不提交该文件，不打印 DSN。
Python 不持有控制面管理员权限；Mock 路径不需要任何模型密钥，真实 Provider 尚未启用。

```bash
bash scripts/setup.sh
bash scripts/bootstrap.sh --fresh-test-db
source scripts/dev-env.sh
docker compose --env-file .local/db.env -f deploy/compose.yaml config --quiet
go build ./...
go run ./cmd/mgarctl health
```

Compose 配置使用 `--quiet` 验证，避免输出插值后的本地测试密码。
数据库卷是本项目专用合成数据，不连接生产服务；不要删除用户其他容器/卷。
进程和服务需要在新任务中重新启动，安装好的文件和依赖不代表服务仍在运行。

P1 的 CLI 是本机受信控制面入口，读取 `.local/db.env` 中的合成库连接配置。
它直接调用 Go 的受控状态写入协议；不要把此入口或数据库管理员凭据交给 Worker、
模型输出或非受信用户。完整身份认证与 Capability Gateway 仍属于后续阶段。

```bash
go run ./cmd/mgarctl submit --fixture incident --provider mock --max-delegations 24
```

`submit` 只持久化离线固定设计 fixture：A（research）与 B（data）汇入 C（report）。
它创建 Run、预算、校验后的计划和 pending 任务；后续 daemon 扫描才重建 ready 状态。
它不启动 Worker，不生成真实研究结果或报告。
`--run-id UUID` 可显式指定新 Run ID；省略时生成新 ID。重复 ID 被拒绝，已有历史不会覆盖。
`--max-delegations 24` 是默认委派次数上限，不能据此推导模型费用授权。

从提交结果读取 `run_id`，在当前 shell 设置 `RUN_ID` 后执行：

```bash
go run ./cmd/mgarctl status "$RUN_ID"
go run ./cmd/mgarctl cancel "$RUN_ID"
go run ./cmd/mgarctl resume "$RUN_ID"
```

这三条分别展示接口用法：`cancel` 冻结非终态 Run；`resume` 仅允许满足安全与预算条件的
`blocked` / `interrupted` Run。已取消或其他终态 Run 不能恢复，按上面的顺序执行时最后一条会被拒绝。
`status` 查询持久状态。提交 fixture 后如没有测试预置的结果，状态保持等待执行，不能自动完成。

```bash
go run ./cmd/orchestratord --once
go run ./cmd/orchestratord
```

`--once` 执行一轮持久恢复扫描后退出；默认模式提供 HTTP 服务并在后台扫描非终态 Run。
扫描重建 ready、处理已存在的受信 verdict 并推进安全 checkpoint，不执行 Agent、工具或模型。
`mgarctl health` 无需数据库连接，返回 `status=ok, provider=mock`。
Daemon 启动时打开数据库并 Ping；HTTP `GET /healthz` 也检查 PostgreSQL 连通性，
数据库不可用时返回 503。HTTP 健康状态不代表 Run 已完成。

```bash
go test ./cmd/... ./internal/scheduler/... ./tests/e2e/...
bash scripts/test.sh --offline
bash scripts/accept.sh P1-05
```

第一条为 P1-05 专项验收；离线脚本运行已有完整 Go/Python 检查并核实没有 Skip/Xfail。
`accept.sh` 只接受与当前源文件、契约 JSON、架构文档和部署配置指纹一致的通过记录。
修改实际受测输入后应重新测试；`evidence/` 与 `TASK_QUEUE.md` 的验收状态不参与输入指纹。
完整阶段边界与故障实验解释见 [limitations.md](limitations.md)，真实运行结果见
[`evidence/batches/P1-05/`](../evidence/batches/P1-05/)。

## Python SDK 专项检查

P2 SDK、Provider 的边界与接口说明见 [agent-sdk.md](agent-sdk.md)。
模型决策 Mock 之外的受信工具实际执行，结果仍须独立验证。

```bash
source scripts/dev-env.sh
python -m pytest -q tests/unit/test_agent_loop.py tests/unit/test_agent_loop_boundaries.py
python -m pytest -q tests/unit/test_providers.py
```

Provider 本轮只测试本机 HTTP stub，真实付费请求仍关闭。

ResearchAgent 专项检查：

```bash
python -m pytest -q tests/e2e/research_test.py tests/security/test_research_scope.py
```

这些检查实际读取合成文档，Mock 只替代模型决策；没有联网检索或模型效果实验。
