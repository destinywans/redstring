# 前端与 UI 改造计划及进度 · 2026-09-23

> 2026-09-23：P2/P3 已被 [AI-first 纠偏基线](2026-09-23-ai-first-realignment.md) 取代，本文件仅保留 P1 记录。

分支：`codex/frontend-ui-refactor`（基于 `codex/wan-110-observability-workspace@bfd0e38`）
范围：**只改前端 `web/`，后端、迁移、OpenAPI 不动。** 目标是做出一版可体验的改造效果，用户看完再决定下一步。

## 已完成

- [x] 在 `codex/wan-110-observability-workspace` 提交原有未提交改动：`bfd0e38 chore(WAN-110): snapshot in-progress observability and delivery work`（含可观测、暂停中的交付代码及文档，未测试）。
- [x] 从该提交新建分支 `codex/frontend-ui-refactor`。
- [x] 已清理首次提交失败时残留的 `.git/index.lock` 和临时对象。

## 构建环境（已解决）

- 2026-09-23 在本机 macOS 会话中直接构建，`pnpm --dir web build` 可用；预览用 `.claude/launch.json` 的 `web`（`pnpm --dir web dev`，5173，未提交）+ `?preview=1`。
- 首次构建失败原因：WAN-110 快照引用的 `observability-workspace.css` 从未落盘，已按现有 token 重建（`4f0c97b`）。

## P1 完成情况（2026-09-23，均已提交未推送，构建通过，浏览器 `?preview=1` 浅/深色核对）

- [x] P1-1/2/3 `116f44c`：一级导航收为 工作台 / 调查 / 应用与资源 / 观测与告警 / 知识与集成（集成中心放入第五组，未另设一组）；交付隐藏；审计进设置工作区。`/settings`→`/automation`；集成统一为 `/integrations/{tools,mcp,models,knowledge}` 共享页签；`?tab=`、`/ai-extensions/mcp` 旧链接带查询串重定向。`/infrastructure` 直接进真实页并补页内视图页签，设计稿移到 `/design-review/infrastructure`。调查分组在画布外会自动展开。
- [x] P1-6 `f796765`：zustand toast 队列（最多 4 条、同文合并、悬停暂停、错误 10 秒）+ 常驻 `aria-live` 区域。
- [x] P1-5 `9227242`：`components/ui/status-badge.tsx`（真实 / 示例 / 已配置未验证 / 未知 / 异常），替换资源、RBAC、知识库、集成、概览里的零散 Mock/演示标签；样板见 `/design-review/components`。
- [x] P1-4 `c26b1cf`：调查页闭环状态条，由真实证据、action-plans、execution 推导；审批/恢复以 `?panel=approval|recovery&plan=` 右侧面板打开（`features/actions/routes.tsx` 适配独立页与嵌入）。

待用户体验后决定是否继续 P2。未做：真实后端联调下的闭环条验证（仅 preview 数据核对）。

## 待做（按顺序，均只动前端）

### P1 可见的结构与 UI 改动
1. **导航精简**：`lib/navigation.ts` 的一级分组从 11 个收为 5 个——工作台 / 调查（会话画布、审批、恢复、记忆、Skills、Prompt）/ 应用与资源 / 观测与告警（基础设施并入）/ 知识；交付中心在暂停期间隐藏；审计移入设置。旧 URL 保留重定向。
2. **路由命名修正**：`/settings`（实为自动化任务）改为 `/automation`；`/integrations` 由 `SettingsPage` 渲染一事理顺；集成中心的 `?tab=` 与 `/ai-extensions/mcp` 统一。旧路径保留重定向。
3. **`/infrastructure` 默认进入真实页**：`InfrastructureReviewPage` 移到 `/design-review/infrastructure`，不再需要 `?mode=live`。
4. **闭环状态条**：调查页顶部增加「异常 → 取证 → 提案 → 审批 → 执行 → 180 秒验证」，审批 / 恢复以右侧聚焦面板打开，不跳出画布。
5. **统一的数据可信度标识**：`StatusBadge` 覆盖真实 / 示例（is_mock）/ 已配置未验证 / 未知 / 异常。
6. **Toast 队列**：替换 App 中单条、6.5 秒的 toast，改为 zustand 队列并加 `aria-live`。

### P2 客户端数据架构
7. 每个领域建 `queryOptions` / queryKey 工厂，替换手写 key，失效按前缀进行。
8. 拆分 `WorkspaceContext`：Session / Scope（URL 为唯一来源）/ Toast。
9. `useCan(permission)` 基于 `/rbac/me`，替换 35 处 `role === "admin"`。
10. 统一 `lib/format.ts`（日期、数字）、`domain/labels.ts`（状态文案）、`toUserError()`（替换 24 处 `String(e)`）。
11. 调查页 SSE 改为按事件 `setQueryData` 增量更新；轮询间隔收为常量，页面隐藏时暂停。

### P3 工程整理
12. 拆分 `InvestigationPage.tsx`（2696 行）为 canvas / chat / evidence / stream / layout。
13. 组件库统一：删除 `components/ui.tsx` 里与 `components/ui/*` 重复的 Button / Modal；三种下拉收敛为一种；调查页内的 MetricChart 改用共享组件。
14. 样式：stylelint 禁止在 token 文件之外写裸 hex 和 px 字号，逐步收敛约 120 个硬编码色值和 27 个 `!important`。
15. preview 模式改用 MSW / mock 适配层（features 里有 437 处 preview 分支）。
16. 补 Vitest（search / scope / selection）和 Playwright 冒烟测试加截图对比。

### 暂不做
- OpenAPI 类型生成（需要装新依赖并改契约脚本，等构建环境解决后再评估）。
- 任何后端 / 迁移 / 契约改动。

## 验证方式
每个 P1 项完成后：前端构建通过，并在 `?preview=1` 下截图浅色 / 深色对比；不跑全仓测试，不写业务数据。

## 恢复工作时
重启后在新会话中说「继续 docs/plans/2026-09-23-frontend-ui-refactor.md」；P1 已完成，等用户体验反馈后从 P2-7 开始。
