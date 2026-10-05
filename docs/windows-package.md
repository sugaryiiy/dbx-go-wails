# Windows 桌面版交付说明

本次目标为 **Windows 10/11 x64 的 Database Workbench 桌面程序**，复用现有 Tauri 入口和最新工作台界面。版本基于 DBX 0.6.33。

本次已使用 `x86_64-pc-windows-gnullvm` 目标、llvm-mingw UCRT 工具链从 macOS 交叉构建，并由 `scripts/package-windows.mjs` 组装便携包。交付位置为：

- `target/windows/Database-Workbench-windows-x64/Database-Workbench.exe`
- `target/windows/Database-Workbench-windows-x64.zip`

## 当前验证状态

截至 2026-10-05：

| 项目 | 状态 |
| --- | --- |
| 前端 TypeScript 类型检查 | 通过 |
| 工作台、操作搜索、字体与快捷键相关回归 | 12 个文件、109 项通过 |
| Windows x64 可执行文件构建 | 通过，PE32+ / AMD64 / GUI 程序 |
| Windows 10/11 真机启动、WebView2 与数据库操作 | 尚未验收 |
| Windows 安装包、代码签名与正式发布 | 本次交付未签名便携 ZIP，未制作安装程序或发布 |

上述前端检查在 macOS 上运行，不能替代 Windows 桌面验收。包内 `SHA256SUMS` 和 `build-manifest.json` 记录文件校验值和构建信息。

## 使用条件与数据目录

运行环境为 Windows 10/11 x64，桌面窗口依赖 Microsoft Edge WebView2 Runtime。便携版仅包含应用程序时，仍需目标电脑已安装可用的 WebView2；它不等同于包含 WebView2 的离线安装包。真正的 Windows 启动与运行尚未验证。

请完整解压便携包，保留程序旁的 `WebView2Loader.dll`、`libunwind.dll`、`LICENSE` 和空的 `portable.dbx` 标记。没有 `uninstall.exe` 标记时，程序会将数据放在可执行文件旁的 `data` 目录，故应解压到当前用户可写的位置。`DBX_DATA_DIR` 可显式指定独立数据目录，并优先于便携模式。

基础 SQLite 功能不需要另外放置数据库服务。DuckDB、JDBC 等可选驱动按项目原有驱动管理流程安装；不应把未安装的可选驱动视为已经随单个 `.exe` 交付。

## 与当前 macOS 包的差别

- 工作台布局、操作搜索、字体设置和 SQL 编辑界面来自同一套前端。Windows 使用现有 Windows 快捷键；macOS 的 DataGrip 快捷键预设不会直接照搬。
- 现有 Tauri 欢迎页不显示“打开示例数据库”；该入口目前仅由 Web 后端提供。Windows 验收需新建独立的 SQLite 连接或打开自己的测试库。
- Tauri 已内置 MCP HTTP 服务，与界面共用数据库状态及 SQL 收藏、查询历史能力。首次使用需在 MCP 设置中启用，默认地址为 `http://127.0.0.1:5225/mcp`，按设置页给出的令牌配置客户端。
- macOS Swift 启动器默认使用的 `4227` 端口、自动登录及 `mcp-client.json` 文件不属于 Tauri 的启动流程。Tauri 的工具数量还受编译功能、权限和插件配置影响，不能直接套用 macOS 基础包“28 个工具”的验收数字。

## 构建入口与最小验收

本机可重新运行工作区外的 `../build-dbx-windows.command`。它使用工作区 `.toolchains` 中的 Rust 1.99.0、LLVM MinGW 20260922（Clang 23.1.2）及 CMake 4.4.4，先构建前端，再执行：

```sh
CARGO_PROFILE_RELEASE_OPT_LEVEL=1 \
CARGO_PROFILE_RELEASE_LTO=false \
CARGO_PROFILE_RELEASE_CODEGEN_UNITS=16 \
cargo build --locked -p dbx --release --features custom-protocol \
  --target x86_64-pc-windows-gnullvm
```

上述 Cargo 命令还需要该脚本设置的交叉编译器路径，不能脱离环境直接在另一台 Mac 运行。保留项目全部默认功能；采用较快的本地 release 优化参数，没有启用发布配置的 LTO。首次 Rust 构建耗时 10 分 28 秒；日志在 `target/windows/logs/backend.log`。该耗时不包含工具链下载和前端构建。

仓库已有的 Windows 原生构建入口为：

```powershell
pnpm tauri build --target x86_64-pc-windows-msvc --no-bundle --ci
```

它需要 Windows 的 Rust/MSVC 编译环境、Node.js 22.13+、pnpm 10.27.0 及前端依赖；预期输出为 `target/x86_64-pc-windows-msvc/release/dbx.exe`。此命令描述现有构建方式，不表示本次已成功执行。macOS 交叉构建应另外记录其目标、工具链和输出路径。

在 Windows 的独立测试目录完成以下检查后，才能标记桌面验收通过：

1. 启动程序，确认窗口和最新工作台正常显示，退出后能再次启动。
2. 新建本地 SQLite 测试库，建表、插入、查询并编辑一行，重启后确认数据仍在。
3. 保存和重新打开 SQL，检查查询历史；完成一次 CSV 或 XLSX 导出与导入。
4. 检查 Windows 操作搜索、执行 SQL、字体设置和文件选择对话框。
5. 启用 MCP，验证带令牌的 `initialize`、`tools/list`、SQLite 查询、SQL 收藏及历史搜索；未带令牌请求应被拒绝。

检查应使用临时数据目录，不对现有个人或团队数据库执行测试写入。已有 macOS smoke 记录不能作为 Windows 实测结果。
