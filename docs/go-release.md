# Go/Wails macOS 预览发布

当前发布标签：`go-v0.1.0-preview.20261011.3`。应用版本为 0.1.0，最低 macOS 13。
此预览新增 7 项对象 MCP 工具，支持过程/函数/触发器的只读列表和源码、MySQL/MariaDB 事件计划与状态；
保留对象目录及 PostgreSQL 9.2/17 的表详情与查询补全修复，以及
[迁移状态](../MIGRATION.md) 中记录的集成增量和功能边界。

在 macOS arm64 上安装前端依赖后，先完成源码提交，再构建和打包：

```sh
pnpm --filter dbx... install --frozen-lockfile
node scripts/wails.mjs build
python3 scripts/package-go-macos.py --tag go-v0.1.0-preview.20261011.3 \
  --output build/wails/bin/DBX-Go-macOS-arm64-20261011-mcp-object-tools.dmg
```

脚本要求 Git 工作区没有待提交源码，不会收集连接配置或应用数据目录。
它校验构建应用的签名和架构，创建带 Applications 快捷入口的 DMG，验证镜像完整性，
挂载后再次检查应用签名及可执行文件哈希，并生成 `.dmg.sha256` 和 `.dmg.manifest.json`。
清单记录源码提交、应用版本、最低系统版本、架构和校验结果。

安装时打开 DMG，将 `Database Workbench.app` 拖入 Applications。
本预览使用 ad-hoc 签名，尚未经过 Apple 公证；macOS 可能要求在系统设置的隐私与安全性中允许打开。
应用仍使用原有 Go 数据目录；安装包不包含用户连接或凭据。
MCP 共 20 项工具；已有显式授权列表不会自动增加新工具，需要在设置中勾选后重新连接 MCP 客户端。

发布到 GitHub 时推送当前分支及指向该提交的标签，并上传三个生成文件。
Go 迁移仍在进行，因此应标记为 prerelease。Windows/Linux 安装包需在对应环境另行构建和验收。
