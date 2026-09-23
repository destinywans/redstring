# 前端与 UI 改造计划及进度 · 2026-09-23

分支：`codex/frontend-ui-refactor`（基于 `codex/wan-110-observability-workspace@bfd0e38`）
范围：**只改前端 `web/`，后端、迁移、OpenAPI 不动。** 目标是做出一版可体验的改造效果，用户看完再决定下一步。

## 已完成

- [x] 在 `codex/wan-110-observability-workspace` 提交原有未提交改动：`bfd0e38 chore(WAN-110): snapshot in-progress observability and delivery work`（含可观测、暂停中的交付代码及文档，未测试）。
- [x] 从该提交新建分支 `codex/frontend-ui-refactor`。
- [x] 已清理首次提交失败时残留的 `.git/index.lock` 和临时对象。

## 当前阻塞：构建环境

- Claude 的云端环境和本机 Linux 沙箱都无法访问 npm registry（403 / 无网络），装不了依赖。
- 本机 `web/node_modules` 是 macOS 版本（rolldown、lightningcss、tailwind oxide、TypeScript 7 都是 darwin-arm64 原生包），沙箱里跑不了。
- 可选方案：
  1. 由用户在 Mac 终端执行 `pnpm --dir web build` / `make web-dev`，把报错贴回来；Claude 负责改代码。
  2. 为 Claude 会话开放 npm registry 出网，由 Claude 在云端构建并用 Playwright 截取 `?preview=1` 页面。
  3. 开启电脑操控，由 Claude 在 Mac 终端直接构建。

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
重启后在新会话中说「继续 docs/plans/2026-09-23-frontend-ui-refactor.md」，先确认构建方式（见上面的阻塞一节），再从 P1-1 开始。
