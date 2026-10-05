# DataGrip macOS 快捷键预设

核对日期：2026-10-04。参照 JetBrains DataGrip **2026.2 的 macOS 默认 keymap**，不是另外的“macOS System Shortcuts”或“IntelliJ IDEA Classic (macOS)”方案。官方网页的普通正文可能先显示 Windows 键位，本次同时核对了页面内 `topic-shortcuts` 数据中的 `macOS` 项。

DBX 提供“DataGrip（macOS）”与“DBX 原有键位”两个预设。macOS 新配置默认采用 DataGrip 预设；其他平台继续使用 DBX 原有键位。本预设对齐已有 DBX 操作，不代表实现了 DataGrip 的全部功能。

## 选择与自定义

打开 **设置 → 快捷键 → 快捷键预设**，选择“DataGrip（macOS）”或“DBX 原有键位”，点击应用后生效。仍可搜索操作名称并单独改键，单项恢复默认使用当前预设的默认值。

手动选择预设会替换当前快捷键草稿，包括草稿中的自定义键位；应用之前可以检查和修改。首次升级的自动迁移采用下述保留规则。

符号：`⌘` Command、`⌥` Option/Alt、`⌃` Control、`⇧` Shift、`⌫` Backspace、`⏎` Return。下表中的“原有”列按 macOS 解释，不是 Windows/Linux 键位。

## 常用键位

| 操作 | DataGrip（macOS）预设 | DBX 原有预设 | 对齐说明 |
| --- | --- | --- | --- |
| 执行当前/选中 SQL | `⌘⏎` | `⌘⏎` | 保留 DBX 的选中优先及当前/全部执行设置 |
| 停止当前查询 | `⌘F2` | 未绑定 | 本次增加独立停止操作，调用现有取消执行流程 |
| 打开/收起查询历史 | `⌘⌥E` | 未绑定 | 本次增加历史快捷键；DBX 打开侧边历史面板 |
| 最近页签 | `⌘E` | 未绑定 | DBX 列出当前仍打开的页签；不包含 DataGrip Recent Files 的全部范围 |
| 新建查询 | `⌘⇧L` | `⌘T` | 对齐官方 New Query Console；不是 `⌘N` 或 Classic 方案的 `⌘⇧Q` |
| 数据库侧栏 | `⌘1` | `⌘B` | DBX 当前为展开/收起侧栏；DataGrip 为 Database Explorer 工具窗口操作 |
| 快速打开对象 | `⌘O` | `⌘P` | 对齐官方 Go to Database Object 的入口习惯；DBX 搜索范围更广，见下文 |
| 搜索操作 | `⌘⇧A` | 未绑定 | 对齐官方 Find Action 入口键；当前接入 17 项工作台常用操作 |
| 文件内容搜索 | `⌘⇧F` | `⌘⌥F` | 对应 Find in Files 习惯，使用 DBX 已配置的文件搜索范围 |
| 格式化 SQL | `⌘⌥L` | `⌘⇧F` | 官方 Reformat Code |
| 编辑器查找 | `⌘F` | `⌘F` | 相同；侧栏等区域仍按焦点路由各自的查找 |
| 编辑器替换 | `⌘R` | `⌘R` | 相同；此键不用于 DBX 全局刷新 |
| 跳转到行 | `⌘L` | `⌘⌥G` | 对齐官方 Go to Line/Column 的主要入口；DBX 操作为跳转到行 |
| 标签访问历史后退/前进 | `⌘[` / `⌘]` | `⌃⌥←` / `⌃⌥→` | 按键对齐 Back/Forward；DBX 保存标签访问历史，不等同于 DataGrip 全部光标位置导航 |
| 上一个/下一个标签 | `⌘⇧[` / `⌘⇧]` | 同左 | 相同 |
| 标签切换器 | `⌃Tab` | `⌃Tab` | 沿用 DBX 标签切换器 |
| 行注释 | `⌘/` | `⌘/` | 官方 Comment with Line Comment |
| 块注释 | `⌘⌥/` | `⇧⌥A` | 官方 Comment with Block Comment |
| 触发基础补全 | `⌃Space` | `⌥/` | 官方 Basic Completion；若系统输入法占用，可单独改键 |
| SQL 意图操作 | `⌥⏎` | `⌘⇧⏎` | 官方 Show Intention Actions；只显示 DBX 已有 SQL 意图操作 |
| 在下方插入新行 | `⇧⏎` | `⇧⏎` | 官方 Start New Line |
| 复制当前行/选中内容 | `⌘D` | `⌘D` | 官方 Duplicate Line or Selection；具体操作仍由编辑器选区决定 |
| 删除当前行 | `⌘⌫` | `⌘⇧K` | 官方 Delete Line，使用 Backspace，不是前向 Delete |
| 合并行 | `⌃⇧J` | `⌘J` | 官方 Join Lines |
| 向上/向下移动行 | `⌥⇧↑` / `⌥⇧↓` | `⌥↑` / `⌥↓` | 官方 Move Line；不是按语法结构移动整个语句 |
| 扩展选择 | `⌥↑` | `⌥W` | 官方 Extend Selection；DBX 仍使用现有选择扩展逻辑 |
| 缩小选择 | `⌥↓` | 未绑定 | 按本次扩展历史逐步恢复先前选区，不推测任意手工选区的缩小范围 |
| 选择下一个相同词/全部相同词 | `⌃G` / `⌃⌘G` | 同左 | 对齐官方 Add Selection for Next Occurrence / Select All Occurrences |
| 切换大小写 | `⌘⇧U` | `⌘⇧U` | 相同 |
| 折叠/展开全部 | `⌘⇧−` / `⌘⇧+` | `⌃⌥[` / `⌃⌥]` | 官方明确使用 **NumPad − / NumPad +**；DBX 按组合键匹配，不区分数字键盘与主键盘，属于兼容映射 |

编辑器还保留 CodeMirror 的查找下一个 `⌘G` 和上一个 `⌘⇧G`，与官方 macOS 查找键位一致；它们来自编辑器内置查找 keymap，不是预设表中的独立可配置动作。撤销 `⌘Z`、重做 `⌘⇧Z`、全选 `⌘A`、保存 `⌘S`、设置 `⌘,` 沿用原有键位。

## DBX 保留项与明确差异

- **全局刷新数据继续使用 `F5`。** DataGrip 的 Database Explorer 和数据编辑器 Refresh 为 `⌘R`。DBX 已有按焦点路由：编辑器内 `⌘R` 打开替换，结果表格或恢复后等待加载的表数据页中 `⌘R` 刷新数据；树元数据刷新未统一映射为 `⌘R`。原生“重新加载工作台”已移到 `⌃⌘R`，避免抢走这些操作。
- **`⌘E` 打开最近页签。** 松开 Command 后列表保持打开，使用方向键选择、Return 确认、Escape 取消。列表只包含当前仍打开的 DBX 页签，不等同于 DataGrip 包含其他近期文件的 Recent Files。原有 `⌃Tab` 切换器继续可用。
- **执行计划在 DataGrip 预设中不绑定默认键。** 原有 DBX 预设仍使用 `⌘E`，新预设将它让给最近页签；执行计划仍可从工具栏使用或单独改键。所查官方“Query execution plan”页未标注 Explain Plan 默认快捷键，因此没有推测其他映射。
- **快速打开范围不同。** DataGrip 的 `⌘O` 是数据库对象导航，`⌘⇧O` 是文件导航，`⌘⌥O` 是符号导航。DBX 的 `⌘O` 打开同一个对象模式对话框，可检索连接、数据库、表、例程、SQL 文件、SQL 库及插件入口，未拆成这三种独立导航。
- **让出已有冲突键。** DataGrip 预设清空“向上/向下复制行”的默认键，为移动行让出 `⌥⇧↑/↓`；清空“切换到第一个标签”的默认键，为数据库侧栏让出 `⌘1`。功能没有删除，仍可自定义其他键。
- **搜索操作有明确范围。** `⌘⇧A` 或全局工具栏“更多 → 搜索操作”打开 17 项常用动作，支持中文名称、英文关键词、分组搜索及当前实际快捷键展示。不可用项显示原因且无法执行；方向键选择、Return 执行、Escape 关闭，也可使用鼠标。关闭时先恢复原焦点，再派发操作，支持继续打开其他对话框。动作范围见 `workbenchActions.ts`，并不包含 DataGrip 或 DBX 的全部 IDE 操作。
- **缩小选择依赖扩展历史。** `⌥↓` 逐步退回 `⌥↑` 创建的历史选区；手动移动光标、改变选区或编辑文本会清空历史。无历史时保持不变并消费该快捷键，不会继续触发移动行；未实现对任意手工选区的结构推测。
- **保留 DBX 特有动作。** 在新结果标签执行 SQL 的 `⌘\`、SQL 星号展开、AI 面板、表格/侧栏专用操作等继续保留。“发送选中内容到 AI”在 DataGrip 版本 2 预设中默认清空键位，为 Find Action 让出 `⌘⇧A`；功能入口仍在，可自定义其他键。DBX 原有预设仍为 AI 操作保留 `⌘⇧A`。
- **数据库侧栏与导航语义不同。** `⌘1` 当前是 DBX 的侧栏展开/收起；`⌘[/]` 回溯标签访问记录。它们不会自动获得 DataGrip 工具窗口聚焦或编辑位置历史的全部行为。

本次未增加包含已关闭文件等范围的完整 DataGrip Recent Files、覆盖全部 IDE 操作的 Find Action、`⌘⇧⏎` Complete Current Statement、`⌘⌥⏎` 在当前行之前插入新行、`⌘⇧↑/↓` 按语法结构移动语句，或独立的 Go to Declaration。DataGrip 的双击 Shift“Search Everywhere”也未接入双击修饰键触发。

`⌃Space` 可能与 macOS 输入法切换冲突，功能键也可能需要按住 `Fn`。这是系统键盘设置影响；可以在 DBX 中单项改键。浏览器宿主可能保留 `⌘L`、`⌘O` 等组合，Web 版与原生 WKWebView 包需要分别验证，不能只用编辑器单元测试判定都能拦截。

## 首次升级与持久化

1. 未保存预设信息的 macOS 旧配置，首次加载时自动采用 DataGrip 预设。
2. 首次采用 DataGrip 时，以 DBX 原默认值为基准；DataGrip 版本 1 升级至版本 2 时，以版本 1 默认值为基准。与相应旧默认值相同的键位按默认项迁移；不同的自定义键位，以及主动清空的绑定，保留原值。
3. 新默认键若会与保留的自定义操作冲突，清空该新默认绑定，让用户自定义优先；不会用新默认覆盖已有自定义。
4. 已完成当前版本迁移的配置，不反复执行迁移。版本 1 的 DataGrip 配置会升级一次至版本 2，增加搜索操作 `⌘⇧A`、缩小选择 `⌥↓`，并清空仍为旧默认值的 AI 发送键位；自定义仍按上述规则保留。预设标识、版本和完整键位一起保存。
5. 手动切回“DBX 原有键位”并应用，可以恢复原预设；之后仍可逐项修改。

将 DataGrip macOS 配置同步或导入到 Windows/Linux 时，会转成 DBX 预设并保留可识别的自定义值，不在这些平台启用 macOS 预设；若再导回 Mac，可手动选择 DataGrip 预设。

迁移依据已保存的键位值判断，无法区分“用户主动设置为恰好等于旧默认值”和“从未修改默认值”。当前迁移版本为 `2`；实现见 `shortcutPresets.ts`，动作与作用域定义见 `shortcutRegistry.ts`。若搜索操作的新默认键被保留的自定义键占用，可先使用“更多 → 搜索操作”，再按需重新绑定。

## 官方核对来源

- [Predefined macOS keymap](https://www.jetbrains.com/help/datagrip/reference-keymap-mac-default.html)：编辑、查找替换、导航、注释、选择、移动行、折叠及工具窗口。
- [Run queries](https://www.jetbrains.com/help/datagrip/run-a-query.html)：`Console.Execute.Multiline`、`Stop`、`Console.History.Browse` 的 macOS 键位。
- [Query consoles](https://www.jetbrains.com/help/datagrip/query-consoles.html)：`Jdbc.OpenConsole.New` 的 macOS 键位为 `⌘⇧L`；`Console.Open` 的 `⌘⇧F10` 是选择/打开查询 console，不能混为新建。
- [Search in the IDE](https://www.jetbrains.com/help/datagrip/search-in-ide.html)：数据库对象、文件、符号及操作搜索的区别。
- [Database Explorer](https://www.jetbrains.com/help/datagrip/database-explorer.html) 与 [Data editor and viewer](https://www.jetbrains.com/help/datagrip/data-editor-and-viewer.html)：各自上下文中的 Refresh `⌘R`。
- [Query execution plan](https://www.jetbrains.com/help/datagrip/query-execution-plan.html)：Explain Plan 功能入口；本页未标注其默认快捷键。

## 本轮验收

2026-10-04 已在 macOS 原生 WKWebView 窗口实际通过以下键盘操作：

| 快捷键 | 实际验收内容 |
| --- | --- |
| `⌘⇧L` | 新建查询页签 |
| `⌘⌥L` | 格式化 SQL |
| `⌘Return` | 执行查询，返回 4 行结果 |
| `⌘R` | 编辑器内打开替换；表格上下文刷新数据 |
| `⌘E` | 打开最近页签，松开 Command 后弹层保持显示 |
| `⌘⌫`、`⌘Z` | 删除当前行，再撤销恢复 |
| `⌘⌥E` | 打开查询历史 |
| `⌘1` | 切换数据库侧栏显示 |
| `⌘O` | 打开对象搜索 |
| `⌘F2` | 取消正在执行的 SQLite 递归查询 |

设置中的预设选择、保留自定义键位和迁移相关测试已通过。上述原生操作只代表已列出的场景；全量绑定、Windows/Linux 真机行为，以及 `⌃Space` 与输入法的实际冲突尚未完成验收。本文仍区分官方键位、DBX 近似映射与未实现功能，不把自动测试通过视为所有平台和宿主的页面验收通过。

版本 2 新增的操作搜索、选区缩小及迁移逻辑已实现，相关自动测试通过。搜索测试包括中英文过滤排序、键盘/鼠标执行、禁用与空结果、输入法确认和重复 Enter 防护，以及关闭后恢复焦点再打开后续对话框；选区缩小测试覆盖扩展历史恢复与历史失效行为。

版本 2 在 macOS 原生包中已完成以下实际验收：

| 场景 | 实际结果 |
| --- | --- |
| 版本 1 配置升级 | 旧配置自动迁移后，`⌘⇧A` 能打开搜索操作 |
| 菜单入口 | 全局工具栏“更多 → 搜索操作”可打开弹窗 |
| 不可用操作 | 表数据页中 execute 项禁用，按 Return 不执行操作且保持搜索窗口 |
| 英文搜索刷新 | `refresh table` 命中并刷新表数据，验收表显示 4 行 |
| 中文搜索新查询 | “新建查询”命中并创建临时查询页签 |
| 按当前语句执行 | 无选区时从动作搜索选择 execute，只执行当前 SQL，验收查询返回 4 行 |
| 按选区执行 | 选中第二条语句后搜索“执行”，仅执行选中 SQL，返回 1 行 |
| 空结果 | 搜索不存在的关键词，显示空结果提示 |
| 后续对话框焦点 | 通过搜索打开新建连接后，焦点位于数据库类型搜索框 |
| 扩展与缩小选区 | 两次 `⌥↑` 从光标选中 `select` 词，再扩为当前语句；两次 `⌥↓` 逐级恢复词和光标；第三次无历史保持不变，不改动 SQL 行 |

4 行和 1 行是上述验收 SQL 的结果，不是所有查询的固定行数。本轮没有逐项原生验收全部 17 个常用操作，其他操作、其他选区组合和平台仍以相应验证范围为准。完整英文短语 `new connection` 已通过动作目录自动测试和最终包原生验证，可以匹配并打开新建连接对话框，焦点位于数据库类型搜索框。
