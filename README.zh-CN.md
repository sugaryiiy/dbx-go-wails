# DBX Go + Wails

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

macOS 产物：`build/wails/bin/DBX Go.app`。
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
- 连接配置、SQL 文件、历史记录、设置及布局，加密持久化和跨进程数据目录锁。
- 可重复使用的 SQLite 示例库。
- CSV/TSV/XLSX 参数化事务导入；CSV/XLSX/JSON/文本查询导出及进度、取消。
- 有主键表格增删改、复制 INSERT/UPDATE 和补偿 SQL。
- Wails 原生窗口控制、文本剪贴板、文件对话框和导出保存。

SQLite 已做真实数据库与 HTTP 集成测试；MySQL/PostgreSQL 尚无实库验证。
导入导出当前限制单任务 10 万行、32 MiB；导出使用只读查询。
其他数据库、TLS/SSH、高级结构编辑、AI/MCP、插件及发布更新仍待迁移。
后端未迁移能力会明确返回错误。详细状态见 [MIGRATION.md](MIGRATION.md)。

## 验证

```sh
pnpm test:go
pnpm test:go-smoke  # 自动创建临时 Go 服务并清理，只操作测试数据
go test -mod=mod -race ./internal/... ./cmd/...
# 先构建前端资源，再测试桌面绑定：
go test -mod=mod -tags desktop .
```

默认启动、构建和生效的 GitHub CI 已使用 Go/Wails。旧 Rust 源码与 `legacy:*`
命令保留作迁移参考，旧工作流移至 `.github/legacy-workflows`，不自动运行。

## 来源

基于 [t8y2/dbx](https://github.com/t8y2/dbx) 的
`977ca7bd382bff0bc300d453bd6f336580b5a56c` 和已有本地工作台修改，保留原
[LICENSE](LICENSE)、版权声明及贡献者署名。原 Rust 版独立保存在
[sugaryiiy/dbx-rust](https://github.com/sugaryiiy/dbx-rust)。
