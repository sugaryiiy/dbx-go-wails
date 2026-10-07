# Go + Wails 迁移状态

更新：2026-10-07。

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
| 安全连接 | 严格 TLS 1.2+/mTLS、最多 4 跳 SSH、known_hosts 或 SHA256 主机校验、加密共享隧道；详见 [安全连接](docs/go-secure-connections.md) |
| 查询 | CRUD、批量语句、事务执行、超时、取消、结果限额、分页和基础排序/EXPLAIN SQL 构建 |
| 表格编辑 | 有主键表格增删改、复制 INSERT/UPDATE、补偿 SQL；无主键更新/删除明确拒绝 |
| 写入保护 | 只读查询、生产连接保护和限时解锁；生产库列表非空时保守锁整个连接 |
| 元数据 | 基础数据库/表/列/索引/外键；SQLite/MySQL DDL；PostgreSQL 普通表/视图/物化视图结构 DDL、表达式及 INCLUDE 索引、复合外键；高级对象仍有限制 |
| 应用数据 | 加密保存连接/凭据、设置、布局、SQL 文件和历史；原子落盘、跨进程锁 |
| 示例 | 真实 SQLite customers/products/orders/order_items，重新打开保留修改 |
| 导入 | CSV/TSV/XLSX 预览与映射、参数化事务写入、失败整体回滚、取消/进度 |
| 导出 | CSV/XLSX/JSON/文本、只读查询、Web 下载和 Wails 原生保存、取消/进度 |
| MCP | 官方 Go SDK，HTTP/stdio，13 个数据库工具；默认关闭、只读授权、Token 轮换、权限撤销及审计，详见 [使用说明](docs/go-mcp.md) |
| CI | Go 后端多平台测试，macOS Wails 构建；配置已写入，远端执行尚未验证 |

## 当前限制

- 自定义 DSN/URL 参数、初始化脚本、附加数据库和其他数据库引擎尚未迁移。
- 安全连接仅支持 MySQL/MariaDB、PostgreSQL。SSH 不读取 OpenSSH 配置别名，不支持 ProxyCommand、exec 回退、HTTP/SOCKS 代理、HTTP 隧道、LAN 监听或交互式 MFA；agent 当前仅支持 Unix socket。
- PostgreSQL DDL 不是完整备份：不导出数据、权限、所有权或序列当前位置，引用对象需预先存在；分区/继承/外部表、RLS、触发器/规则等高级对象明确拒绝，详见 [DDL 边界](docs/go-secure-connections.md#postgresql-结构-ddl)。
- 持久事务会话、显式 BEGIN/COMMIT 脚本和 CREATE TRIGGER 尚未支持；事务执行接口可用。
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
- MCP 桌面实测：在隔离示例目录通过设置向导授权并启用服务；HTTP 和 CLI 客户端均发现 13 工具，查询订单总数得到 5，DELETE 被拒绝；重启恢复服务和 Token。
- MCP 最终全量 Go race、go vet、Vue 类型检查与相关前端回归、macOS Wails 构建通过。界面截图：`outputs/mcp-settings-smoke.png`（本地产物）。

- TLS/mTLS 协议测试验证受信证书、主机名/CA 不匹配拒绝、客户端证书配对及旧 TLS 版本拒绝；SSH 协议测试覆盖多跳、known_hosts/指纹、密码/私钥/agent、取消和连接隔离。相关 race 测试通过。
- MySQL 8.4、PostgreSQL 17 实库矩阵：直连 TLS、单跳/双跳 SSH 均通过 CRUD、事务回滚、只读写入与 `FOR UPDATE` 拒绝、超时/显式取消、关闭及重连；检查实际会话 TLS cipher，错误 CA/主机名被拒绝。以 `DBX_TEST_DATABASE_CONFIGS` 指定测试配置文件启用。
- MySQL 8.4、PostgreSQL 17 TLS 实库 MCP：持久化连接配置执行 `SELECT 42`、只读写入拒绝、不保存密码时拒绝复用均通过 race 测试。共享隧道加密持久化、原子校验、引用更新/删除解析和凭据脱敏测试通过。
- PostgreSQL 17 TLS 实库：复合外键、索引表达式/opclass/排序/INCLUDE、主键 INCLUDE 列归属、自引用外键，以及表/视图/物化视图 DDL 删除重建均通过；验证 serial/identity、生成列、默认值和约束实际行为。相关测试以 `DBX_TEST_POSTGRES_DSN` 启用，race 检查通过；另验证 PostgreSQL 17 的默认统计目标 NULL 不被改写成禁用统计的 0。

此状态按已运行检查记录；未覆盖的能力不视为已经迁移或验证。

## 来源和后续验收

上游 [t8y2/dbx](https://github.com/t8y2/dbx)，基线
`977ca7bd382bff0bc300d453bd6f336580b5a56c`，保留 LICENSE 和署名。
后续按原 Rust 版本逐项补齐功能和跨平台验证，再移除仅作参考的旧 Rust/Tauri 文件。
