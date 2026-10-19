# WAN-96 · 记忆页通用搜索与筛选

- 需求：[WAN-96](https://linear.app/wansss/issue/WAN-96)
- 分支：`codex/wan-96-search-filters`
- 设计基线：[通用搜索与筛选](../design/search-filters.md)
- 状态：源码、受影响构建和记忆页限定浏览器检查完成，等待产品体验。提交与推送结果回读记录在 WAN-96；未合并、未正式发布。

## 已实施

记忆与上下文页面移除重复的应用/环境旧范围栏，环境只服从左上角的全局范围。保留应用多选，通过搜索、应用、类型和生命周期组成统一筛选栏；分类标签独立于筛选栏。条件进入 URL，刷新/后退回填；已应用条件可逐个移除，清除筛选保留 `environment_scope` 和当前分类。

新增 `FilterBar`、`FilterSearch`、`FilterSelect`。统一 36px 高度、14px 字体、12px 水平内边距、8px 圆角和间距。Radix Popover 使用 Portal 与碰撞处理，同一栏仅一个浮层打开，面板使用真实 `--panel` 变量避免透明叠字。多选暂存后点“应用”，取消、Escape 或点击外部丢弃草稿；单选直接提交。组件保留未知但已选的 ID，不因候选变化静默扩大范围。

分类页签新增单个 Tab 停靠点、左右方向键和 Home/End 切换；结果区可键盘聚焦。记忆列表悬停和选中背景改用现有的 `--panel`，不再引用未定义的 `--surface` 变量。

仅记忆页采用 `usePageScope({ globalEnvironmentOnly: true })`，其他页面的旧链接、具体执行目标和默认筛选语义保持原状。记忆页会清理旧 URL 中环境覆盖参数，避免看不见的局部环境与全局环境不一致。

## 验证证据与边界

- 2026-09-20，定向预览快照运行 `pnpm --dir .local/home-review/web build --outDir <repo>/web/dist --emptyOutDir` 成功，包括 `tsc -b` 与 Vite 构建（1864 modules）。更新的是已有 Go 服务使用的 `web/dist` 静态资源，未重启后端。
- 快照继续剥离历史尚未验收的 delivery `RunEvidence` 与 reconcile 按钮；未将临时剥离改回正式源码。此次没有修改交付功能、后端、数据库、模型或运行环境。
- 受影响已跟踪文件 `git diff --check` 通过。新组件和记忆页文件已格式化。
- 主代理复用现有浏览器完成限定检查：应用多选取消不提交，应用后 URL/标签同步；连续打开类型/状态仅一个 dialog；Escape 返回触发器焦点；关键词 Enter 提交并刷新回填，清除保留 Demo 全局环境。
- 应用候选搜索、ArrowDown 聚焦、Space 选择可用；分类页签 ArrowRight/Home 切换生效，上下文分类不显示知识专属类型/状态。旧 environment=demo 参数被清理，environment_scope=demo 保留，页面只有一个环境控件。
- 实际截图观察浅/深主题：浮层背景不透明、文字有内边距、筛选与页签分行。390×844 读取 innerWidth=390、scrollWidth=390，弹层 x=14、width=320，未超出视口；完成后恢复默认视口及浅色主题。截图保存在忽略目录 .local/ui-evidence/wan-96/，不提交运行数据。
- 未运行全量测试、后端测试、真实模型调用或任何业务写操作。

## 未覆盖

未覆盖所有浏览器、减少动效实际运行、所有页面迁移或完整权限矩阵。未知选中 ID 保留、无授权环境不扩大范围已做源码检查，未构造额外业务数据验证。本轮限定检查不代替产品验收。

人员、团队/部门、时间以及其他页面批量迁移仍为设计方向，尚未实现。此次组件不改变后端查询字段或权限。
