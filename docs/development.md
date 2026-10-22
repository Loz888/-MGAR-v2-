# 本地开发与验收

Version: development.v1

已有独立检出目录即可工作；云任务已隔离，不需要另外创建 Git worktree。
Go 工具链固定 1.27.3（Linux amd64 官方包 SHA-256
`65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40`），
Python 固定 3.12，依赖由 `uv.lock` 锁定，PostgreSQL 镜像摘要见 `deploy/compose.yaml`。

当前云环境先 `source scripts/dev-env.sh`，然后 `uv sync --frozen`、`go build ./...`。
标准系统若已有相同 Go 版本，可直接使用本机 Go；本环境系统的 `/usr/bin/go` 不是 Go 编译器。
`scripts/dev-env.sh` 激活工作区工具链、虚拟环境与缓存，并读取未跟踪的 `.local/db.env`。
只读主目录下不能使用默认 uv 缓存；不要关闭 TLS、包校验或 Go checksum database。

本地测试凭据必须通过安全随机数生成到 `.local/db.env`（0600），包括
`MGAR_TEST_DB_PASSWORD` 与仅指向 `127.0.0.1:55432/mgar_test` 的 `MGAR_TEST_DATABASE_URL`。
不提交该文件，不打印 DSN。应用配置拒绝非回环或非 mgar_test 前缀的数据库。
Python 不持有控制面管理员权限；Mock 路径不需要任何模型密钥，真实 Provider 尚未启用。

```bash
source scripts/dev-env.sh
docker compose --env-file .local/db.env -f deploy/compose.yaml up -d --wait
docker compose --env-file .local/db.env -f deploy/compose.yaml config --quiet
go build ./...
go test -race ./...
go vet ./...
python -m pytest -q
ruff check .
mypy
go run ./cmd/mgarctl health
go run ./cmd/orchestratord
```

健康接口为 `GET /healthz`，返回 `status=ok, provider=mock`；它不宣称完整编排能力。
Compose 配置使用 `--quiet` 验证，避免输出插值后的本地测试密码。
数据库卷是本项目专用合成数据，不连接生产服务；不要删除用户其他容器/卷。
进程和服务需要在新任务中重新启动，安装好的文件和依赖不代表服务仍在运行。
