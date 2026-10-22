# Go + Wails 迁移状态

更新：2026-10-22。

## 2026-10-22 仓库清理收尾与 agents 评估

Rust 工作区与 Tauri 壳删除后的收尾清理：注解类型对齐测试改对 Go 结构体
（`internal/server/cache_annotations.go`，解码器同样启用
DisallowUnknownFields，语义不变）；删除前端 Rust 时代的 32 工具 MCP 清单
及其开关逻辑，设置界面实际使用的是 `goMcpPolicy` 的 20 工具清单，与 Go
服务端注册表对齐；删除 `.github/legacy-workflows/` 与失效的
`.github/scripts/`（仅保留 macOS MCP 签名、agents 版本维护、i18n 解析三组
在用脚本）；删除已失效的 `build-macos-mcp` 打包脚本（其后端阶段依赖已删除
的 Rust `dbx-web`）与 Tauri/Win7 安装器模板测试；docs-export 产物目录迁至
`apps/desktop/src/docs-export/generated`；`wails.json` 的 productVersion 与
`package.json` 对齐为 0.6.33。

`agents/` 评估结论：**保留**。`internal/agent` 在运行时管理这套 Java 驱动，
JDBC 类连接（Oracle、SQL Server 及多种国产数据库）依赖它，前端驱动路径与
发布脚本也引用它，不属于可删除的遗留代码。

DuckDB、TDengine：驱动源码已随瘦身删除，前端连接类型保留；如需恢复，用 Go
原生驱动重写，不再恢复 Rust 实现。此项不作为 macOS 应用的完成标准。

## 2026-10-12 常见错误中文提示

统一错误翻译入口增加数据库未连接、连接中断、超时、写入锁定、事务状态、结果过期、
分页与 SQL 分隔符校验、MCP 授权及网络连接错误的提示。简体和繁体中文界面使用对应
中文提示，英文界面保留英文；已知认证、权限、SQL 语法及 TLS 错误先显示本地化说明，
保留原始错误码、对象名和诊断内容。未知驱动消息保持原文，不替换 SQL 或标识符中的词句。
连接错误气泡、统一错误面板、Redis 控制台与多库执行面板复用翻译入口，保留底层原始错误。
750 项错误翻译、协议错误解析、语言占位符、错误复制和相关界面回归、全量 Vue 类型检查通过。
本次预览标签为 `go-v0.1.0-preview.20261012`。

## 2026-10-11 工作台提示调整

移除工作台顶部的 Go/Wails 迁移预览提示，释放该行占用的空间；中英文界面均生效。
功能范围与迁移限制仍记录在本文档中。本次预览标签为 `go-v0.1.0-preview.20261011.4`。

## 2026-10-11 存储过程与事件 MCP

Go MCP 新增 `dbx_list_objects`、`dbx_list_routines`、`dbx_get_routine_source`、
`dbx_list_events`、`dbx_list_triggers`、`dbx_get_object_source` 和 `dbx_get_event_info`，
工具总数增至 20。HTTP/stdio 复用相同定义和保存权限；工具仅列出账户可见对象、
读取定义与事件计划/状态，不调用或修改例程、触发器和事件。

列表支持类型/名称筛选与分页，定义支持按字符分段读取；PostgreSQL 例程使用精确
重载签名，触发器用所属表区分同名对象。事件工具专用于 MySQL/MariaDB 定时事件，
未包含 PostgreSQL event trigger。自由文本进行凭据脱敏；对象身份保持完整以支持后续读取。

设置页增加全部 7 项工具的中英文授权选项，保留已有显式工具授权列表，工具数按
实际选项计算。5 个专项前端测试文件的 48 项检查、全量 Vue 类型检查和变更格式检查通过。
MySQL 8.4、PostgreSQL 9.2/17 和 SQLite 隔离实库验证通过，包含禁用事件的计划/状态、
重载函数、同名触发器、跨 schema 列表、长源码分页与凭据脱敏。HTTP/stdio 均发现
全部 20 项工具，读取定义不会执行对象；只读及连接/工具授权拦截通过。
全量 Go race、go vet、原生绑定测试和隔离 HTTP 冒烟通过。
本次预览标签为 `go-v0.1.0-preview.20261011.3`；安装包验收另记录于本地产物。

## 2026-10-11 对象列表与详情修复

修复对象浏览器和侧栏复用表列表，导致存储过程、函数、触发器、事件与序列
始终显示 0 的问题。MySQL 对象目录增加例程、触发器和事件；PostgreSQL 增加例程、
序列和非内部触发器；SQLite 增加触发器。分类筛选和分页应用于完整对象列表，
表列表仍只返回表和视图。对象数量取决于当前数据库、schema 和账户可见范围。

源码读取按对象种类选择 MySQL SHOW CREATE，支持事件详情；PostgreSQL 使用
例程签名和触发器所属表区分重载或同名对象，列表与源码使用一致的目录快照。
例程分类检测实际系统目录字段，兼容版本号较旧但包含过程目录的 PostgreSQL 衍生服务。

MySQL 8.4、PostgreSQL 9.2/17 与 SQLite 隔离实库验收通过，包含禁用事件、
重载函数、自定义参数类型及同名触发器。160 项相关前端测试、全量 Go race、
go vet、原生绑定测试、Vue 类型检查和隔离 HTTP 冒烟通过。
本次预览标签为 `go-v0.1.0-preview.20261011.2`，平台和签名方式与前一版相同。

## 2026-10-11 PostgreSQL 兼容性修复与预览发布

修复旧版 PostgreSQL 表提示缺失、表详情为空的问题。元数据查询根据服务器版本选择
列、主键、索引、约束、序列和分区目录字段；补全直接查询系统目录，支持表名、字段、
视图、函数、序列及 schema，表注释接口已接入前端。

PostgreSQL 9.2 与 17 的隔离实库测试通过，覆盖表/列注释、表达式索引、复合外键、
补全筛选及限额、结构 DDL 删除重建和外键约束。macOS 原生界面已验证表结构、DDL、
表名补全及别名后的字段补全。与补全和结构编辑有关的 242 项前端测试通过。
发布前全量 Go race、go vet、原生绑定测试、Vue 类型检查及隔离 HTTP 冒烟通过；
桌面适配器、MCP、连接编辑器、SQL 文件和 InfluxDB 预览相关的 329 项前端回归通过。

预览发布标签为 `go-v0.1.0-preview.20261011`，包含下文的 Go 迁移集成增量。
macOS arm64 安装包使用 `Database Workbench.app`，最低 macOS 13；采用 ad-hoc 签名，
尚未公证。当前发布只提供此平台的安装包。
打包说明及构建、校验命令见 [Go macOS 发布](docs/go-release.md)。

## 2026-10-09 集成增量

本轮继续迁移并打包当前 Go/Wails 版本，仍未达到原版全部功能等价。
以下为本轮新增代码范围；安装包的构建和验收结果另记录于
`outputs/install-macos-20261009.json`（本地产物）。

- 查询标签页独立物理连接、跨请求显式事务、手动事务接口、结果分页快照、
  SQLite trigger 脚本、结构部署和失败回滚。
- 原生 SQL 文件授权读写、版本冲突检测、目录操作；受管 SQL 文件上传、执行、
  取消和进度；SQLite 在线备份与恢复、损坏源检查和恢复期间连接隔离。
- SQLite 安全表重建、结构对比与同步 SQL、数据对比、数据库文档、
  高级 SQL 构建和 EXPLAIN；具体数据库及对象边界仍由接口校验。
- 驱动智能体安装与 JSON-RPC 运行时、扩展数据库接入、HTTP 服务驱动和消息队列适配。
  智能体引擎需要对应驱动包；接口测试不代表每种外部数据库均已实库验收。
- AI 配置、提供商协议、流式请求、工具循环、提示模板、技能、缓存及文档注释；
  插件包校验、运行时、数据授权和市场接口已通过本轮专项回归。
- Web 子路径和 Docker/多平台 CI 配置已加入；远端 CI、签名公证和跨平台安装仍需单独验收。

云同步、定时数据库备份、部分高级数据库管理、托盘通知、脱离窗口、自动更新及
其余未覆盖接口仍在迁移中。静态接口清单见
[go-migration-inventory.json](docs/go-migration-inventory.json)；声明数量不是功能完成率。

本轮 macOS arm64 安装验证：全量 Go race、go vet、桌面 Go 测试、Vue 类型检查、
553 项前端回归和隔离 HTTP 冒烟通过；Wails 应用已打包、签名校验并安装至
`/Applications/DBX Go.app`，原生窗口显示工作台就绪。
旧应用已保留本地备份。DMG 位于 `build/wails/bin/DBX-Go-macOS-arm64-20261009.dmg`。

下文保留 2026-10-07 已验收基线和当时限制；涉及上面新增范围时，以本轮实现、
测试结果和明确的接口错误为准。

## 当前实现

已新增可独立运行的 Go 主后端和 Wails 桌面程序，保留 Vue 3 界面。
桌面前端通过 Wails 内部资源处理器调用同进程 Go 服务，默认不监听 HTTP 端口；主动启用 MCP 后会启动独立回环监听；
Web 模式使用同一服务实现并要求登录。默认命令和 CI 已切换到 Go/Wails。
原 Rust 源码仍保留作对照，原版另存于
[sugaryiiy/dbx-rust](https://github.com/sugaryiiy/dbx-rust)。

这不是原版百余种数据库及全部功能的完整替代版本；未实现接口返回明确错误。

| 范围 | 状态及边界 |
| --- | --- |
| 桌面运行 | Wails 2.16.0；macOS 原生应用已打包、启动并执行示例查询 |
| 窗口/剪贴板/文件 | 原生窗口控制、置顶、文本剪贴板、打开/保存对话框、导出文件保存 |
| 原生数据库 | SQLite、MySQL/MariaDB、PostgreSQL；SQLite、MySQL 8.4 和 PostgreSQL 17 已有实库验证，MariaDB 尚未单独实库验收 |
| 新增数据库 | Redis 单机键浏览/基础类型编辑、MongoDB 单节点文档/聚合/索引、ClickHouse HTTP SQL/元数据；TLS/SSH 与写入保护，详见 [范围及实库测试](docs/go-native-engines.md) |
| 安全连接 | 严格 TLS 1.2+/mTLS、最多 4 跳 SSH、known_hosts 或 SHA256 主机校验、加密共享隧道；详见 [安全连接](docs/go-secure-connections.md) |
| 查询 | CRUD、批量语句、事务执行、超时、取消、结果限额、分页和基础排序/EXPLAIN SQL 构建 |
| 表格编辑 | 有主键表格增删改、复制 INSERT/UPDATE、补偿 SQL；无主键更新/删除明确拒绝 |
| 写入保护 | 只读查询、生产连接保护和限时解锁；生产库列表非空时保守锁整个连接 |
| 元数据 | 基础数据库/表/列/索引/外键；SQLite/MySQL DDL；PostgreSQL 普通表/视图/物化视图结构 DDL、表达式及 INCLUDE 索引、复合外键；高级对象仍有限制 |
| 应用数据 | 加密保存连接/凭据、设置、布局、SQL 文件和历史；原子落盘、跨进程锁 |
| 示例 | 真实 SQLite customers/products/orders/order_items，重新打开保留修改 |
| 导入 | CSV/TSV/XLSX 预览与映射、参数化事务写入、失败整体回滚、取消/进度 |
| 导出 | CSV/XLSX/JSON/文本、只读查询、Web 下载和 Wails 原生保存、取消/进度 |
| MCP | 官方 Go SDK，HTTP/stdio，20 个数据库工具；包括对象、存储过程/函数、表触发器及 MySQL/MariaDB 定时事件的只读列表/详情/定义；默认关闭、只读授权、Token 轮换、权限撤销及审计，详见 [使用说明](docs/go-mcp.md) |
| CI | Go 后端多平台测试，macOS Wails 构建；配置已写入，远端执行尚未验证 |

## 当前限制

- 自定义 DSN、任意 URL 参数、初始化脚本、附加数据库和其他数据库引擎尚未迁移；MongoDB 仅开放认证库、SCRAM 机制、directConnection=true 参数。
- 安全连接支持 MySQL/MariaDB、PostgreSQL、Redis、MongoDB、ClickHouse。SSH 不读取 OpenSSH 配置别名，不支持 ProxyCommand、exec 回退、HTTP/SOCKS 代理、HTTP 隧道、LAN 监听或交互式 MFA；agent 当前仅支持 Unix socket。
- Redis 暂限单机及 string/hash/list/set/zset（原子编辑需要 Redis 7+）；MongoDB 暂限单节点 SCRAM；ClickHouse 无事务、直接表格编辑或通用导入。新增三类引擎尚未接入 MCP；现有关系型表格编辑、导入等能力不自动适用于它们。
- PostgreSQL DDL 不是完整备份：不导出数据、权限、所有权或序列当前位置，引用对象需预先存在；分区/继承/外部表、RLS、触发器/规则等高级对象明确拒绝，详见 [DDL 边界](docs/go-secure-connections.md#postgresql-结构-ddl)。
- 持久事务会话、显式 BEGIN/COMMIT 脚本和 CREATE TRIGGER 尚未支持；事务执行接口可用。
- MCP 可读取例程、触发器和 MySQL/MariaDB 定时事件元数据及定义，但不调用或修改这些对象；PostgreSQL event trigger 尚未通过 MCP 暴露。读取使用保存账户的可见范围，新增工具不自动加入已有显式授权列表。
- 编辑保存遇到主键记录已被外部删除时，当前沿用原版 0 行受影响结果，尚无专门的并发冲突提示。
- MySQL 参数化导入限 InnoDB 表；含反斜杠的手写 SQL 字面量、可执行版本注释和复合 CREATE 例程明确拒绝，避免方言歧义。
- 高级 SQL 解析/语义引用、复杂结构编辑、数据对比/同步及高级管理操作尚未完整迁移。
- 单个导入/导出任务最多 100,000 行、32 MiB。导入仅 UTF-8 和 XLSX；导入建表、
  冲突跳过/合并、选列导出、导出 setup SQL 未实现。NULL 默认标记为 `\N`。
- 保存密码关闭时不落盘该连接内的凭据，重连重新输入；独立共享隧道凭据仍按其自身配置保存。应用状态加密使用数据目录中的密钥，尚未接入系统钥匙串。
- AI、原版其余 MCP 工具、插件/市场、远程代理、消息队列工具、备份/同步、脱离窗口、托盘、通知、
  自动更新和原发布流程仍待迁移；旧界面的部分高级入口仍会收到未实现错误。
- Web 子路径部署、Docker 镜像、Windows/Linux 桌面包装和正式签名/公证未完成验证。
- Rust 数据目录没有自动导入到 Go 版；两个版本使用隔离存储。

## 已执行验证

- `go test -mod=mod -race ./internal/... ./cmd/...`：通过。
- SQLite 真正执行 CRUD、外键级联、参数化导入、事务失败回滚、超时/取消、生产保护、
  大整数、二进制、中文、NULL 和空字符串等边界。
- HTTP 完整冒烟：登录、配置保存/连接、查询/元数据/DDL、分页、错误恢复、SQL 文件、
  历史记录、CSV/XLSX 导出上传再导入、断开重连和清理：全部通过。
- HTTP 加密持久化、跨进程锁、Origin/方法约束及 Wails 网络访问隔离测试：通过。
- Vue 生产构建、31 项相关前端测试、Go 原生保存测试、macOS `.app` 打包：通过。
- 开发入口 `dev:wails` 为调试构建后直接启动（无热重载），已实跑；Web Vite 代理登录/退出和跨站拒绝已验证。
- 全量 `vue-tsc --noEmit` 在 Node 8 GiB 堆内存上限下通过；`pnpm typecheck` 已使用该上限（默认 4 GiB 对当前工作台源码不足）。
- 原生保存窗口实际导出 CSV，落盘后提示完成；文件解析验证 8 行、10 列、963 字节。取消和写入失败保护有 Go/前端测试覆盖。
- 原生应用真实打开示例库并执行联表查询，返回 8 行结果。

- MCP：官方 SDK 新旧协议、stdio 实际子进程、HTTP → stdio 代理、只读拦截、批量回滚、权限范围和失败审计均通过。
- MCP 生命周期：Token 轮换、停用和权限收紧可取消真实 SQLite 长查询；应用退出等待审计落盘后释放存储锁；域名反向代理、精确 Host/Origin 校验通过真实 TCP 测试。
- MCP 桌面初版实测：在隔离示例目录通过设置向导授权并启用服务；HTTP 和 CLI 客户端均发现当时的 13 项基础工具，查询订单总数得到 5，DELETE 被拒绝；重启恢复服务和 Token。新增 7 项对象工具的验证不包含在这条记录中。
- MCP 基础版全量 Go race、go vet、Vue 类型检查与相关前端回归、macOS Wails 构建通过。界面截图：`outputs/mcp-settings-smoke.png`（本地产物）。

- TLS/mTLS 协议测试验证受信证书、主机名/CA 不匹配拒绝、客户端证书配对及旧 TLS 版本拒绝；SSH 协议测试覆盖多跳、known_hosts/指纹、密码/私钥/agent、取消和连接隔离。相关 race 测试通过。
- MySQL 8.4、PostgreSQL 17 实库矩阵：直连 TLS、单跳/双跳 SSH 均通过 CRUD、事务回滚、只读写入与 `FOR UPDATE` 拒绝、超时/显式取消、关闭及重连；检查实际会话 TLS cipher，错误 CA/主机名被拒绝。以 `DBX_TEST_DATABASE_CONFIGS` 指定测试配置文件启用。
- MySQL 8.4、PostgreSQL 17 TLS 实库 MCP：持久化连接配置执行 `SELECT 42`、只读写入拒绝、不保存密码时拒绝复用均通过 race 测试。共享隧道加密持久化、原子校验、引用更新/删除解析和凭据脱敏测试通过。
- PostgreSQL 17 TLS 实库：复合外键、索引表达式/opclass/排序/INCLUDE、主键 INCLUDE 列归属、自引用外键，以及表/视图/物化视图 DDL 删除重建均通过；验证 serial/identity、生成列、默认值和约束实际行为。相关测试以 `DBX_TEST_POSTGRES_DSN` 启用，race 检查通过；另验证 PostgreSQL 17 的默认统计目标 NULL 不被改写成禁用统计的 0。

- Redis 7.4、MongoDB 8、ClickHouse 25.8 实库：TLS、SSH、基本读写、只读保护、取消、错误 CA 拒绝及结果限额通过 race 测试。Redis 覆盖五类基础类型、二进制、TTL、并发逻辑库隔离和 ACL；MongoDB 覆盖 Extended JSON/复杂 ID 往返与索引；ClickHouse 覆盖 UInt64/Decimal、元数据及服务端查询终止。
- 三类新增引擎 HTTP 验证：登录、连接加密保存后重启、TLS 重连、原生接口、临时解锁/重新上锁及 MCP 隔离；ClickHouse 四种查询结果导出和文本 EXPLAIN 已实测。完整 Go race、go vet、Vue 类型检查、467 项相关前端回归及 Wails 构建通过。
- macOS 原生应用实测：Redis 字符串编辑保存、MongoDB 文档编辑保存及 Shell 查询、ClickHouse 表格浏览/分页 SQL 查询与精确数值显示。截图存于本地 `outputs/redis-go-preview.png`、`outputs/mongo-go-preview.png`、`outputs/clickhouse-go-preview.png`。

此状态按已运行检查记录；未覆盖的能力不视为已经迁移或验证。

## 来源和后续验收

上游 [t8y2/dbx](https://github.com/t8y2/dbx)，基线
`977ca7bd382bff0bc300d453bd6f336580b5a56c`，保留 LICENSE 和署名。
后续按原 Rust 版本逐项补齐功能和跨平台验证，再移除仅作参考的旧 Rust/Tauri 文件。
