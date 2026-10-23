# MagpieDB

保留 Vue 3 界面，使用 Go 主后端和 Wails 桌面壳。新的启动和构建入口不需要
Rust、Cargo 或 Tauri，桌面应用也不需要另起后台服务。

**当前已经可以运行，但尚未完成原 DBX 的全部功能迁移。**
[迁移状态及限制](MIGRATION.md) · [原 Rust 仓库](https://github.com/sugaryiiy/dbx-rust)

## 启动和构建

安装 Go 1.25+、Node.js 22.13+、pnpm 10.27.0，以及
[Wails 平台依赖](https://wails.io/docs/gettingstarted/installation)。
仓库脚本固定使用 Wails 2.16.0，无需另装全局 Wails CLI。

```sh
pnpm install --frozen-lockfile
pnpm dev           # 同时启动 Go 后端和 Vue 开发服务器
pnpm dev:wails     # 调试构建后启动原生应用，不含热重载
pnpm build:wails   # 构建桌面应用
```

macOS 产物：`build/wails/bin/MagpieDB.app`，要求 macOS 13+。
Web 开发默认地址：`http://127.0.0.1:5174`。
本地登录密码保存在 `.dbx-dev/go/login-password`。
可用 `DBX_GO_PORT`、`DBX_GO_BACKEND_PORT`、`DBX_GO_DATA_DIR` 调整开发配置。

独立 Web 服务：

```sh
pnpm build:ui
pnpm build:go
./build/bin/dbx-web
```

默认监听 `127.0.0.1:4224`。可设置 `DBX_PASSWORD`；未设置时生成本地密码文件，
启动日志只打印文件位置。通过 `DBX_DATA_DIR`、`DBX_PORT`、`DBX_BIND_ADDR`、
`DBX_STATIC_DIR` 配置服务。Go 数据目录与 Rust 版分离，同目录禁止多实例同时写入。

## 已实现

- SQLite、MySQL/MariaDB、PostgreSQL 的原生 Go 连接、查询、事务、取消、超时及基础元数据。
- Redis 键浏览和基础类型编辑、MongoDB 文档/聚合/索引、ClickHouse HTTP SQL 与元数据，均支持 TLS/SSH，见 [新增数据库说明](docs/go-native-engines.md)。
- Elasticsearch、Consul、Nacos、Meilisearch、InfluxDB 等 HTTP 服务型连接由 Go 服务驱动实现。
- TLS/mTLS、最多 4 跳 SSH 及加密共享隧道配置，见 [安全连接说明](docs/go-secure-connections.md)。
- PostgreSQL 普通表、视图、物化视图的结构 DDL、索引和复合外键；高级对象有明确限制。
- 连接配置、SQL 文件、历史记录、设置及布局，加密持久化和跨进程数据目录锁。
- 可重复使用的 SQLite 示例库。
- CSV/TSV/XLSX 参数化事务导入；CSV/XLSX/JSON/文本查询导出及进度、取消。
- 表格增删改、复制 INSERT/UPDATE 和补偿 SQL；无主键时使用原始行值定位，保存前检查重复匹配，实际写入影响多行时整批回滚。
- Wails 原生窗口控制、文本剪贴板、文件对话框和导出保存；macOS 菜单栏托盘，以及带未保存提醒的退出/隐藏关闭流程。
- Go MCP：20 个数据库工具、HTTP/stdio、连接和工具授权、Token 认证及查询审计。

SQLite 已做真实数据库与 HTTP 集成测试；MySQL 8.4、PostgreSQL 17 已通过直连 TLS、
单跳/双跳 SSH 的 CRUD、回滚、只读及取消实库验证，也通过 MCP TLS 验证。
PostgreSQL 另已验证 DDL 重建，mTLS 有协议集成测试覆盖。
Redis 7.4、MongoDB 8、ClickHouse 25.8 已通过真实 TLS/SSH、读写及只读保护测试。
导入导出当前限制单任务 10 万行、32 MiB；导出使用只读查询。
其他数据库、高级结构编辑、AI、插件及发布更新仍待迁移。
后端未迁移能力会明确返回错误。详细状态见 [MIGRATION.md](MIGRATION.md)。

## MCP 接入

在「设置 → MCP → 权限授权」选择连接和工具并应用，再到「接入」开启服务。
桌面默认地址为 `http://127.0.0.1:5225/mcp`，Web 使用现有服务的 `/mcp`。
页面提供客户端配置与 Token 管理；`pnpm build:mcp` 构建 stdio CLI。
详细步骤、工具和 SQL 边界见 [Go MCP 使用说明](docs/go-mcp.md)。

## 验证

```sh
pnpm test:go
pnpm test:go-smoke  # 自动创建临时 Go 服务并清理，只操作测试数据
go test -mod=mod -race ./internal/... ./cmd/...
# 先构建前端资源，再测试桌面绑定：
go test -mod=mod -tags desktop .
```

默认启动、构建和生效的 GitHub CI 已使用 Go/Wails。旧 Rust 源码、`legacy:*`
命令与上游旧工作流已在 Go 迁移落地后移除，仍可在 git 历史中查阅。

## 来源

基于 [t8y2/dbx](https://github.com/t8y2/dbx) 的
`977ca7bd382bff0bc300d453bd6f336580b5a56c` 和已有本地工作台修改，保留原
[LICENSE](LICENSE)、版权声明及贡献者署名。原 Rust 版独立保存在
[sugaryiiy/dbx-rust](https://github.com/sugaryiiy/dbx-rust)。
