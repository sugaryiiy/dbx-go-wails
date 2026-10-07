# Go MCP 使用说明

Go/Wails 版使用官方 MCP Go SDK，实现 Streamable HTTP 和 stdio；无需 Rust 或 npm MCP 服务。
服务默认关闭，首次授权默认为只读且不允许任何连接。本文适用于 Go 版；仓库中旧版 MCP 文档和 npm 安装流程属于 Rust 迁移参考。

## 在桌面应用启用

1. 在 DBX 中保存并打开一个数据库连接。可先用 SQLite 示例库验证。
2. 打开「设置 → MCP → 权限授权」，选择允许的连接和工具，点击「应用」。
3. 切换到「接入」，开启 Go MCP 服务并保存。默认地址 `http://127.0.0.1:5225/mcp`。
4. 复制对应客户端配置，将 Token 占位符替换为页面中的 Token，或使用客户端支持的环境变量方式。

Token 默认遮罩，配置模板不会自动包含真实 Token。桌面仅允许回环 IP，关闭 DBX 后服务停止；设置会加密保存，下次启动恢复。端口占用会显示错误，不影响数据库工作台。
修改权限、轮换 Token 或关闭 MCP 会取消正在进行的 HTTP 工具请求。轮换后客户端必须更新 Token。

## HTTP 客户端

支持 Streamable HTTP 的客户端可使用以下通用配置；具体顶层格式以客户端要求为准。VS Code 使用顶层 `servers`；页面提供两种格式。

```json
{
  "mcpServers": {
    "dbx": {
      "type": "http",
      "url": "http://127.0.0.1:5225/mcp",
      "headers": { "Authorization": "Bearer replace-with-your-mcp-token" }
    }
  }
}
```

Web 版在工作台同一个 HTTP 服务的 `/mcp` 路径提供 MCP。先登录工作台，在设置中应用权限，填写实际请求的 Host（包含非默认端口），再启用。远程部署使用 HTTPS 反向代理。允许的 Host/Origin 必须精确匹配，不接受 `*`；普通桌面客户端不发送 Origin，无需为其配置 Origin。反向代理应保留已配置的 Host，浏览器跨源客户端还要填写其完整 Origin。

## stdio 客户端

```sh
pnpm build:mcp
# 确保客户端进程环境中已有 DBX_MCP_TOKEN：
./build/bin/dbx-mcp-go --url http://127.0.0.1:5225/mcp
```

CLI 是长运行协议进程，stdin/stdout 供 MCP 客户端使用。`--url` 将 stdio 转发至已运行 DBX；Token 只从 `DBX_MCP_TOKEN` 读取。仅回环地址允许 HTTP，其余地址必须 HTTPS；不跟随重定向。

```json
{
  "mcpServers": {
    "dbx": {
      "command": "/absolute/path/to/dbx-mcp-go",
      "args": ["--url", "http://127.0.0.1:5225/mcp"],
      "env": { "DBX_MCP_TOKEN": "replace-with-your-mcp-token" }
    }
  }
}
```

Windows 使用构建出的可执行文件路径。也可先退出 DBX，再通过 `dbx-mcp-go --data-dir /absolute/path/to/go-data` 独占打开现有 Go 数据目录。该模式沿用已保存权限，不启动 HTTP 监听；未配置权限时拒绝访问。同一目录有运行中的应用时会报锁冲突。Rust 数据目录不兼容。

## 已实现工具

| 工具 | 用途 |
| --- | --- |
| `dbx_list_connections` | 仅列出授权连接的脱敏摘要 |
| `dbx_list_databases` | 列出可访问数据库 |
| `dbx_list_tables` | 表和视图元数据 |
| `dbx_describe_table` | 列、索引及受支持的附加元数据 |
| `dbx_get_table_ddl` | SQLite/MySQL 表 DDL |
| `dbx_list_indexes` | 表索引 |
| `dbx_get_schema_context` | 有限大小的表和列上下文 |
| `dbx_execute_query` | 一条查询或已授权写入语句 |
| `dbx_execute_batch` | 原子事务批量执行 |
| `dbx_list_saved_sql` | 授权范围内 SQL 文件摘要 |
| `dbx_get_saved_sql` | 读取授权 SQL 文件 |
| `dbx_save_sql` | 新建 SQL 文件，不执行、不覆盖；可在数据库只读模式下使用 |
| `dbx_search_history` | 搜索授权连接的查询历史 |

## 权限与当前边界

- 每次请求检查连接、分组、工具及数据库授权。全局列表 `null` 表示全部，空数组表示全部拒绝；未配置策略始终拒绝。
- MCP 使用持久化连接配置建立独立短期连接。界面临时密码、临时修改或写入解锁不会扩大 MCP 权限；网络数据库需保存凭据。只读和生产连接保护始终生效。
- 支持 SQLite、MySQL/MariaDB、PostgreSQL。SQLite 仅支持现有文件的 `main`；网络数据库目前仅支持整个连接的授权/拒绝，不支持选择部分库的隔离。
- 默认只读。写入需要授权；未启用高风险 SQL 时，UPDATE/DELETE 仅接受简单未引用列名与字面量相等、以 AND 组合的 WHERE 条件。无 WHERE、恒真或复杂条件会被拒绝。DDL 需要高风险授权。
- 即使授权高风险 SQL，也不支持任意管理命令、会话控制、外部文件读写、自定义函数、未知函数、例程、触发器或附加数据库。SQL 方言检查采用保守子集，无法确定的语法会明确报错。
- 单查询默认 100 行、最多 1000 行，结果最多 4 MiB，SQL 最多 64 KiB，批量最多 100 条。默认超时继承连接配置，无配置时 300 秒；可设 1–86400 秒，不支持无限超时。
- 批量强制事务，失败整体回滚；不支持继续忽略错误。MySQL 自身 DDL 可能隐式提交，Go 数据库层会拒绝不能保证事务语义的批量操作。
- PostgreSQL 完整 DDL/部分高级元数据尚未实现；describe 返回可用部分，单独请求不支持的能力会报错。
- 已授权工具调用记入现有历史（`source=mcp`），不保存查询结果。数据库输出始终是数据，客户端不应把它当成指令。
- 当前未实现原版全部 MCP 管理、AI、插件或扩展工具。MySQL/PostgreSQL 尚未完成真实数据库服务验收。

## 开发验证

```sh
pnpm test:mcp
pnpm build:mcp
go test -mod=mod -race ./internal/... ./cmd/...
```

测试涵盖官方 SDK 握手/工具调用、实际 CLI 子进程、SQLite 查询/写入保护/事务回滚、鉴权、Token 轮换、权限撤销、持久化、跨进程锁和桌面监听生命周期。
