# Go 安全连接与 PostgreSQL 结构导出

Go/Wails 版已支持 MySQL/MariaDB、PostgreSQL、Redis、MongoDB、ClickHouse 的严格 TLS、客户端证书配置和 SSH 跳板。
MCP 的独立数据库连接目前限 MySQL/MariaDB、PostgreSQL；新增引擎的具体范围见 [数据库说明](go-native-engines.md)。

## TLS 与客户端证书

在连接编辑器的 SSL/TLS 页启用「TLS，并验证服务器身份」，填写数据库真实主机名。

- 最低 TLS 1.2，验证服务器证书链和数据库主机名；证书验证失败会终止连接，不回退到明文。
- CA 路径留空使用 Go 服务所在系统的信任库。填写 CA 文件后，仅使用该 PEM 文件中的 CA，不叠加系统信任库。
- 双向 TLS（mTLS）同时填写客户端证书和私钥路径。二者必须配对；私钥须为未加密 PEM，证书可包含 PEM 证书链。每个文件最多 4 MiB。
- 证书文件和私钥由 Go 进程读取。桌面可使用文件选择器；Web 页填写的是服务器文件路径，不是浏览器所在设备的路径。支持当前用户的 `~/`。
- 关闭 TLS 时必须清空证书路径。当前没有「忽略证书错误」、单独覆盖服务器名称、任意 `sslmode` 或 URL TLS 参数入口。

经过 SSH 时仍按连接中填写的**数据库主机名**验证 TLS，不按跳板地址验证。
例如数据库证书签发给 `db.internal`，数据库主机填 `db.internal`，SSH 主机另外填写。
数据库地址由最后一跳访问；开启 SSH 不会自动开启数据库 TLS。

## SSH 跳板

在连接编辑器的传输设置中添加 SSH，按从本机到数据库的顺序配置跳板。
最多启用 4 跳；每跳填写主机、端口（默认 22）、用户和认证方式。

| 认证方式 | 所需配置 |
| --- | --- |
| 密码 | SSH 密码 |
| 私钥 | 私钥文件路径；加密私钥同时填写口令 |
| 私钥 + 密码 | 两种凭据均提供，先尝试私钥，必要时尝试密码；是否要求多种认证由 SSH 服务端决定 |
| SSH agent | Go 进程可访问的 Unix agent socket；默认 `SSH_AUTH_SOCK`，也可指定路径 |
| 无认证 | 仅用于服务端明确接受无认证的环境，仍须验证主机密钥 |

每跳连接超时默认 15 秒，可设 1–300 秒；实际连接仍受整个数据库连接超时约束。
通过 Go SSH 协议直接转发 TCP，不调用外部 `ssh`，不创建可供其他程序连接的本地转发端口。
「测试 SSH」只验证各跳的身份和认证；使用「测试连接」进一步验证数据库目标、TLS 和数据库凭据。

### 主机身份

默认读取 Go 服务所在设备的 `~/.ssh/known_hosts`，也可填写其他 known_hosts 文件。
未知、变化或已撤销的主机密钥会被拒绝。Go 版不会自动记录首次遇到的密钥，也不弹出交互信任提示。

也可在「固定主机指纹」填写经管理员或可信渠道核对的 `SHA256:…` 指纹。
指纹非空时优先使用指纹，known_hosts 不再参与验证；多跳时分别配置每一跳。
连接错误中显示的指纹仅用于核对，不代表已经可信。

### 共享隧道配置

在设置中的隧道配置管理器保存 SSH 配置，再在多个数据库连接中引用。
共享范围是当前 Go 应用的数据目录；不会同步至其他设备。
配置、密码和私钥口令随应用状态加密落盘；证书和私钥文件保留在原路径，不复制到数据目录。
共享配置最多 128 项，不支持配置之间嵌套引用。

修改共享配置后，已打开的桌面数据库连接需断开重连。
MCP 每次从持久化配置解析共享引用；保存共享配置会取消当时正在运行的 MCP HTTP 请求。
连接的「不保存密码」会清除该连接内的凭据，**不会删除独立保存的共享隧道凭据**。
应用存储密钥仍由 Go 数据目录管理，尚未接入系统钥匙串。

## 当前不支持的连接选项

- SQLite 的网络 TLS/SSH；SQLite 仍使用本地文件。
- 自定义 DSN、任意 URL 参数、初始化脚本、非原生驱动配置和附加数据库。MongoDB 仅允许 `authSource`、`authMechanism=SCRAM-SHA-1|SCRAM-SHA-256`、`directConnection=true` 参数。
- HTTP/SOCKS 代理、HTTP 隧道、`ProxyCommand`、exec 通道回退、局域网转发监听。
- 读取 `~/.ssh/config` 的别名、ProxyJump 等配置；请在界面逐跳填写。
- 键盘交互认证、交互式 MFA 提示、Windows 原生 agent 命名管道；当前 agent 接口为 Unix socket。

旧版配置包含不支持选项时会明确报错。界面可移除旧 SSH 选项；不会静默忽略它们。

## PostgreSQL 结构 DDL

普通表和视图已在 PostgreSQL 9.2 与 17 上验证；物化视图和较新的列、索引选项按服务器版本处理。
表详情、索引、复合外键及查询补全使用兼容旧版本的系统目录查询，无需先展开侧栏即可补全表名和字段。
导出包含列类型、默认值、非空、collation、serial/identity 序列定义与选项、生成列、
主键/唯一/检查/外键/排除约束、索引和注释，以及已实现的列存储、统计目标及 replica identity 设置。
索引保留表达式、列顺序、opclass、排序、INCLUDE 和筛选条件；复合外键保留列对应和动作。

生成的是单个对象的结构脚本。目标 schema、引用的表、类型、函数及非自有序列需预先存在。
脚本不包含数据、所有者、权限或序列当前位置；物化视图用 `WITH NO DATA` 创建，随后可手动刷新。
这不是完整数据库备份，也不覆盖全部数据库级配置。

分区、继承、外部表、临时表、自定义表访问方法、RLS/策略、用户触发器、规则、
扩展所属对象、被修改的内部触发器、无效索引，以及 PostgreSQL 18 的命名 NOT NULL 约束会返回明确不支持错误。
`describe_table` 仍可返回其中已支持的列和索引；单独请求不支持的 DDL 不会返回不完整的建表语句。

## 验证

```sh
go test -mod=mod -race ./internal/database -run 'TestDatabaseTLS|TestSSH|TestReferenceableIndexes'
# 在环境中设置测试专用 DBX_TEST_POSTGRES_DSN 后：
go test -mod=mod -race ./internal/database -run 'TestPostgres' -count=1
# 同时设置测试专用 DBX_TEST_POSTGRES_LEGACY_DSN（9.2）和 DBX_TEST_POSTGRES_DSN（17）后：
go test -mod=mod ./internal/database -run 'TestPostgresMetadataCompatibility' -count=1 -v
# 使用仅供测试的数据库配置 JSON 文件：
DBX_TEST_DATABASE_CONFIGS=/absolute/path/to/test-databases.json \
  go test -mod=mod -race ./internal/database -run TestNetworkDatabaseIntegration -count=1
DBX_TEST_DATABASE_CONFIGS=/absolute/path/to/test-databases.json \
  go test -mod=mod -race ./internal/server -run TestMCPLiveTLSConnections -count=1
```

PostgreSQL 集成测试仅在提供 DSN 时运行，用户需有创建/删除 schema 的权限。
测试使用随机 schema 并清理，检查生成 DDL 是否可执行、重建后结构是否一致，以及约束和生成列的实际行为。
9.2/17 兼容性测试还覆盖列和表注释、复合外键顺序、表达式索引、冷缓存表名/字段补全、
特殊字符筛选、候选限额，以及 DDL 删除重建后的外键实际约束。
网络测试配置文件是 Go `Config` 的 JSON 数组，每项使用 `db_type`（`mysql` 或 `postgres`）、
`host`、`port`、`username`、`password`、`database`、`ssl: true`、`ca_cert_path` 等连接字段。
只使用有建表/删表权限的隔离测试库，将实际凭据文件保存在版本控制之外。
测试自动创建临时 SSH 服务及测试表，无需修改个人 SSH 配置；未配置环境变量时跳过实库测试。

MySQL 8.4、PostgreSQL 17 已通过直连 TLS、单跳和双跳 SSH 的实库矩阵，包括 CRUD、事务回滚、
数据库只读约束、超时/取消、关闭/重连、实际 TLS 会话检查，以及错误 CA/主机名拒绝。
MCP 也完成持久化 TLS 配置、查询与写入保护验证。完整迁移及验收范围见 [MIGRATION.md](../MIGRATION.md)。
