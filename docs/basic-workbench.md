# 基础工作台开发版

直接使用 `.app` 的用户请先阅读 [桌面版使用手册](user-guide.md)；本文面向源码开发与验收。

基于上游 DBX v0.6.33，当前分支 `codex/basic-workbench`。第一阶段聚焦基础功能，暂不开发团队权限、审计和审批。

## 本次交付

沿用 DBX 现成的连接管理、库表/字段/索引浏览、SQL 编辑执行、结果编辑、CSV/Excel 导入导出、查询历史和保存 SQL。这些是上游已有功能，不重复实现。

本次新增：

- `pnpm dev:basic` 同时启动前后端，使用仓库内独立数据目录。
- 欢迎页提供基础操作引导，以及 Web 端“打开示例数据库”入口。
- 示例库包含客户、产品、订单、订单明细与真实外键，可直接查询和编辑；重复打开保留修改。
- 基础 HTTP 验收脚本，使用独立临时 SQLite 验证连接、CRUD、元数据、历史、保存 SQL 和 CSV/XLSX 往返。

## 启动

准备 Node.js 22.13+、pnpm 10.27.0、Rust 工具链及平台编译工具，然后运行：

```sh
pnpm install --frozen-lockfile
pnpm dev:basic
```

当前这台 Mac 已准备好工具链和依赖，也可以直接双击仓库外的 `../start-dbx.command`。该文件使用本机工具链路径，其他电脑使用上面的通用命令。

默认打开 <http://127.0.0.1:5173>，Rust API 在 `127.0.0.1:4224`。第一次启动需要下载并编译 Rust 依赖；终端打印 `Open http://127.0.0.1:5173` 后后端才已就绪。

登录密码首次自动生成，保存到 `.dbx-dev/basic/login-password`，后续启动复用。也可以通过 `DBX_PASSWORD` 环境变量提供自己的本地密码；脚本不会将其打印到日志。运行中的两个服务会随 Ctrl+C 一起退出。

开发数据、连接配置、样例库和备份均放在 `.dbx-dev/basic`，已由 Git 忽略，不使用已安装 DBX 的个人数据目录。端口占用时启动会失败，不会接管其他实例。

可选环境变量：

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `DBX_BASIC_PORT` | `5173` | 前端端口 |
| `DBX_BASIC_BACKEND_PORT` | `4224` | API 端口 |
| `DBX_BASIC_DATA_DIR` | `.dbx-dev/basic` | 独立开发数据目录，相对仓库根目录 |
| `DBX_PASSWORD` | 从密码文件读取/首次生成 | 当前开发实例登录密码 |

基础启动使用 `dbx-web --no-default-features` 加内置 SQLite，减少可选组件构建量；不包含加密 SQLite、DuckDB sidecar、DynamoDB、MQ 控制台等可选模块。需要完整上游功能时使用原有 `pnpm dev:backend` 并配置独立 `DBX_DATA_DIR`。

## 体验流程

1. 登录，点击“打开示例数据库”。系统保存并连接本地 SQLite，打开关联查询；SQL 需手动执行。
2. 展开左侧连接和 `main`，查看 `customers`、`products`、`orders`、`order_items`。
3. 点击执行运行示例 JOIN，初始化数据会返回 8 行；默认执行光标所在语句，也可选中 SQL 后运行。修改筛选条件，练习排序、分页和结果复制。
4. 打开单表视图编辑单元格，预览生成 SQL 后保存。复杂 JOIN 不一定具备可编辑性，以界面提示为准。
5. 使用表的导入入口导入 CSV/Excel；示例文件见 `examples/basic-workbench`。结果导出可选择 CSV/XLSX。
6. 保存常用 SQL，再从 SQL 列表打开；在历史面板找到已执行语句。

“导入连接配置”和“导入表数据”是两个独立操作。Web 的 SQLite 路径属于后端所在机器，不属于浏览器电脑；本地开发时两者恰好位于同一台电脑。

示例接口为 `POST /api/workspace/sample`，返回 `{ connection, sql }`，遵循原有登录和数据迁移流程。公网 `DBX_DEMO_MODE` 下不可使用该写入入口。该接口不重置数据；如果手动更改样例连接目标或占用了样例文件路径，它会报错并保留原内容。

## 验证

```sh
pnpm test:basic-runner
pnpm typecheck
pnpm build

# 仅针对上面新建的独立本地开发实例。
DBX_SMOKE_ISOLATED=1 \
DBX_SMOKE_PASSWORD_FILE=.dbx-dev/basic/login-password \
pnpm test:basic-smoke

cargo test -p dbx-web --no-default-features --features dbx-core/sqlite-bundled sample_workspace
```

smoke 脚本需要与后端共享本地文件系统，只接受 localhost；它创建唯一临时连接和数据库，并在结束时清理自己的资源。不要对团队共享实例运行。SQLite 验收不代表 MySQL、PostgreSQL 或其他驱动已完成兼容性验收，后续接入真实测试实例时应分别执行。

本机验收结果及覆盖范围见 [basic-workbench-verification.md](basic-workbench-verification.md)。

逐项功能、验证状态和后续任务见 [basic-feature-checklist.md](basic-feature-checklist.md)。
