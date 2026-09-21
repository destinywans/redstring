# WAN-96 · 应用列表组件样板

2026-09-21。用户在“前端设计框架比较”讨论后确认先做可交互样板，完成后亲自查看。本轮只批准应用列表、筛选和应用侧卡片，不代表全平台迁移已获确认。

## 方向与边界

- 保留 React/Vite、现有 API 与权限、MiSans、Phosphor、浅色/深色和紫色强调。
- 本地源码组件采用 shadcn 的组合方式，以 Radix Select/Dialog/Popover/AlertDialog 和 cmdk 承担交互。`components.json` 与 `@/` 别名作为后续维护入口；本轮源码按现有 token 适配，不宣称是未经修改的 CLI 模板。
- `ui/` 承担 Button、Input/Textarea、Select、Sheet；`patterns/` 承担 ListPage/ListPanel、FilterPicker/FilterGroup 与未保存提醒。现有 Pagination 保留统一计数/跳页语义，只开放样板所需的 pageSizeControl 槽。
- 其他页面先保留原组件；样板获认可后逐批迁移并删除旧实现，不能把两套 UI 永久并行作为完成状态。
- 不改业务模型、不补假版本/时间，不做 CI/CD，不修改审计分页。

## 本轮样板契约

- 页头：标题、说明、新建操作固定结构。正文与值 400；操作/表头 500；标题 600。样板页标题 22px，控件/列表 14px，辅助 13px；不改变全平台已确认 token。
- 搜索输入即过滤，无额外搜索按钮。部门/负责人未选显示字段名，菜单不增加“全部”；多选使用草稿→应用，取消或点外部丢弃草稿，支持清空。
- 默认顺序显示为“默认顺序”；高级检索按钮在下方展开状态条件。菜单宽度按内容限制，同组只打开一项；选项与触发器有键盘焦点反馈。
- 列表：应用唯一标识、负责人、部门、版本、最近更新时间。说明和开发资料仅在卡片展示；应用名打开卡片，不保留末列箭头。
- 页尾全部在右侧：总条数、每页条数、直接页码、前后翻页。筛选/每页大小变化回第一页，沿用 URL 状态。
- 详情 Sheet 1080px、表单 Sheet 600px，均受视口约束，固定浮在页面上；不改变基础列表宽度。详情保留六个栏目，概览以基础/运行/开发信息分节。
- 表单基础信息、责任归属、可折叠更多资料；头尾固定、中部滚动。原 8 个顶层字段与 10 个 profile 字段及 API 保留。折叠不卸载输入，保存错误内联显示，关闭脏表单先确认，恢复原值解除脏状态。
- 组件状态页 `/design-review/components` 使用明确标注的样例数据、不保存。支持长选项、多选、无结果、禁用、表单提交预览和长内容 Sheet。

## 评审入口

- [应用样板](http://127.0.0.1:18081/applications?environment_scope=all)
- [组件状态](http://127.0.0.1:18081/design-review/components)
- [Linear WAN-96](https://linear.app/wansss/issue/WAN-96)

## 依据与取舍

参考讨论 `前端设计框架比较`（6ab12cda-6144-83e8-a9d3-2cd401ff4848）及当前源码审查：重复的下拉/侧卡片实现和末尾 CSS 覆盖，使页面之间的行为和外观持续分裂。用户已确认先做应用样板；本轮实施不等于所有研究选项均被批准。

- [shadcn/ui](https://ui.shadcn.com/docs)、[Vite 接入](https://ui.shadcn.com/docs/installation/vite)：保留源码控制权，复用交互基元。
- [Radix Primitives](https://www.radix-ui.com/primitives/docs/overview/introduction)：菜单定位、焦点、键盘、可访问性由基元实现。
- [Linear 设计刷新](https://linear.app/now/behind-the-latest-design-refresh)：参考层次、密度与克制的强调，不复制品牌或整站。

未引入整套后台框架，也未重写 React Flow、assistant-ui 或后端。本轮没有引入大型数据表组件，因为现有 5 列列表只需共享版式与分页；后续复杂表格有实际排序/列配置需求时再决定。
