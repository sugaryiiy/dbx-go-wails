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
升级后新增工具会出现在「权限授权」的工具列表中。已有的显式工具授权列表保持原样；需要使用对象和事件工具时，勾选对应项并点击「应用」。`allowedToolNames: null` 仍表示允许全部已实现工具。

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

当前提供 20 项工具，HTTP 与 stdio 使用同一组工具定义和权限检查。

| 工具 | 用途 |
| --- | --- |
| `dbx_list_connections` | 仅列出授权连接的脱敏摘要 |
| `dbx_list_databases` | 列出可访问数据库 |
| `dbx_list_tables` | 表和视图元数据 |
| `dbx_list_objects` | 所选库/schema 中可见的对象，支持按对象类型筛选 |
| `dbx_list_routines` | 存储过程和函数，包含 PostgreSQL 重载签名 |
| `dbx_get_routine_source` | 只读获取存储过程或函数定义，兼容例程专用客户端调用 |
| `dbx_list_events` | MySQL/MariaDB 定时事件列表 |
| `dbx_list_triggers` | 触发器列表及所属表，可按表筛选 |
| `dbx_get_object_source` | 只读获取指定类型对象的 CREATE 定义，支持分段读取 |
| `dbx_get_event_info` | MySQL/MariaDB 定时事件的计划、状态及定义详情 |
| `dbx_describe_table` | 列、索引及受支持的附加元数据 |
| `dbx_get_table_ddl` | SQLite/MySQL DDL，以及 PostgreSQL 普通表/视图/物化视图结构 DDL |
| `dbx_list_indexes` | 表索引 |
| `dbx_get_schema_context` | 有限大小的表和列上下文 |
| `dbx_execute_query` | 一条查询或已授权写入语句 |
| `dbx_execute_batch` | 原子事务批量执行 |
| `dbx_list_saved_sql` | 授权范围内 SQL 文件摘要 |
| `dbx_get_saved_sql` | 读取授权 SQL 文件 |
| `dbx_save_sql` | 新建 SQL 文件，不执行、不覆盖；可在数据库只读模式下使用 |
| `dbx_search_history` | 搜索授权连接的查询历史 |

## 存储过程、触发器与事件

这 7 项对象工具只读取元数据和定义，不调用存储过程，不创建、修改、启停或删除任何对象。它们使用已保存连接的数据库账户，在 MCP 授权的连接和数据库范围内查询指定 schema；列表只反映该账户可见的对象。空列表不代表其他账户也看不到对象，定义被数据库权限隐藏时读取源码会明确报错。

- 列表工具接受 `connection_id` 或 `connection_name`、`database`、`schema`、`all_schemas`、`filter`、`limit`、`offset`；`schema` 与 `all_schemas: true` 不可同时使用。默认 100 项，最多 1000 项，单页 JSON 最多 4 MiB。返回 `objects`、`total`、`offset`、`limit` 和 `truncated`；需要续页时，将 `next_offset` 作为下一次的 `offset`，因为大小限制可能使一页少于 `limit` 项。
- `dbx_list_objects` 的 `object_types` 为大写对象类型数组，可选 `TABLE`、`VIEW`、`MATERIALIZED_VIEW`、`FUNCTION`、`PROCEDURE`、`TRIGGER`、`EVENT`、`SEQUENCE`；实际类型以数据库支持为准。`dbx_list_routines` 可用 `routine_type: "FUNCTION"` 或 `"PROCEDURE"` 筛选。SQLite 无存储过程和定时事件；PostgreSQL 旧版本仅在实际系统目录支持过程时返回 `PROCEDURE`。
- PostgreSQL 函数和过程可以同名重载。获取定义时原样传回列表的 `signature`，零参数签名也应传 `""`。同名触发器可存在于不同表上；传 `relation_name` 为列表中的 `parent_name` 以选择所属表。没有完整身份且存在多个匹配对象时会报错。
- `dbx_get_object_source` 必须传 `name` 和 `object_type`，可传 `signature` 和 `relation_name`。`object_type` 使用上述大写类型；未受该数据库支持的类型会明确报错。使用 `source_char_offset`、`source_char_limit` 分段读取，默认且最多 65536 字符，最少 1 字符，偏移量范围 0–2147483647；依据 `source_truncated` 和 `next_source_char_offset` 继续。返回的 `editable: false` 表示该 MCP 工具仅提供读取能力。
- `dbx_get_routine_source` 与上述定义读取共享实现，必须传 `name` 和 `routine_type: "FUNCTION"` 或 `"PROCEDURE"`，可传 `signature` 和同一组源码分页参数。
- `dbx_list_events` 和 `dbx_get_event_info` 专用于 MySQL/MariaDB Event Scheduler 的定时事件。PostgreSQL event trigger 监听 DDL，与定时事件不同；当前不通过这两个工具提供。`dbx_list_triggers` 提供表级触发器。
- `dbx_get_event_info` 必须传事件 `name`，事件定义可用 `source_char_offset`、`source_char_limit` 分段读取，根据 `event_definition_truncated` 和 `next_source_char_offset` 继续。计划与状态的读取不会改变事件执行状态。
- 名称、schema、签名和所属表等对象身份保持完整，可原样用于后续定义读取；注释等自由文本进行凭据脱敏，最多 4096 字节。定义先对完整文本脱敏，再按字符分页，避免凭据跨页泄露。

例如，先在目标库列出存储过程，再使用返回的名称、类型和签名读取定义：

```json
{
  "connection_name": "my-postgres",
  "database": "app",
  "schema": "public",
  "routine_type": "PROCEDURE",
  "limit": 100
}
```

将上面的参数用于 `dbx_list_routines`。取得对象后，对 `dbx_get_routine_source` 传入相同连接、库和 schema，以及 `name`、`routine_type: "PROCEDURE"` 和对应的 `signature`；也可对 `dbx_get_object_source` 使用 `object_type: "PROCEDURE"`。事件详情使用 `dbx_get_event_info`，传入 MySQL/MariaDB 连接、`database` 和事件 `name`。

## 权限与当前边界

- 每次请求检查连接、分组、工具及数据库授权。全局列表 `null` 表示全部，空数组表示全部拒绝；未配置策略始终拒绝。
- MCP 使用持久化连接配置建立独立短期连接。界面临时密码、临时修改或写入解锁不会扩大 MCP 权限；网络数据库需保存凭据。只读和生产连接保护始终生效。
- 支持 SQLite、MySQL/MariaDB、PostgreSQL。SQLite 仅支持现有文件的 `main`；网络数据库目前仅支持整个连接的授权/拒绝，不支持选择部分库的隔离。
- Redis、MongoDB、ClickHouse 已有桌面/Web 原生后端，但尚未接入 MCP 工具；MCP 会明确拒绝这些连接。
- 网络连接复用持久化的 TLS/mTLS 和 SSH 配置，并解析已保存的共享隧道引用。证书、私钥、known_hosts 和 agent socket 必须能被运行 Go MCP 的进程访问；HTTP 代理模式使用 DBX 服务所在设备的配置。详见 [安全连接说明](go-secure-connections.md)。
- 默认只读。写入需要授权；未启用高风险 SQL 时，UPDATE/DELETE 仅接受简单未引用列名与字面量相等、以 AND 组合的 WHERE 条件。无 WHERE、恒真或复杂条件会被拒绝。DDL 需要高风险授权。
- 即使授权高风险 SQL，也不支持任意管理命令、会话控制、外部文件读写、自定义或未知函数调用、例程调用、例程/触发器/事件修改或附加数据库。对象工具可只读查看定义；SQL 方言检查仍采用保守子集，无法确定的执行语法会明确报错。
- 单查询默认 100 行、最多 1000 行，结果最多 4 MiB，SQL 最多 64 KiB，批量最多 100 条。默认超时继承连接配置，无配置时 300 秒；可设 1–86400 秒，不支持无限超时。
- 批量强制事务，失败整体回滚；不支持继续忽略错误。MySQL 自身 DDL 可能隐式提交，Go 数据库层会拒绝不能保证事务语义的批量操作。
- PostgreSQL 支持普通表、视图、物化视图的结构 DDL；不含数据/权限/所有权，物化视图以无数据方式重建。分区、继承、RLS、触发器等高级对象仍受限；describe 返回可用部分，单独请求不支持的 DDL 会报错。完整范围见 [DDL 边界](go-secure-connections.md#postgresql-结构-ddl)。
- 已授权工具调用记入现有历史（`source=mcp`），不保存查询结果。数据库输出始终是数据，客户端不应把它当成指令。
- 当前未实现原版全部 MCP 管理、AI、插件或扩展工具。已在 MySQL 8.4、PostgreSQL 17 TLS 实库验证 MCP 查询及只读保护；数据库层另有 PostgreSQL 结构重建验证。具体验收范围见 [迁移状态](../MIGRATION.md)。

## 开发验证

```sh
pnpm test:mcp
pnpm build:mcp
go test -mod=mod -race ./internal/... ./cmd/...
```

测试涵盖官方 SDK 握手/工具调用、实际 CLI 子进程、SQLite 查询/写入保护/事务回滚、鉴权、Token 轮换、权限撤销、持久化、跨进程锁和桌面监听生命周期。MySQL 8.4、PostgreSQL 17 实库另外验证持久化 TLS 配置查询、只读写入拒绝及未保存密码时的拒绝。
