# V3 v1.1 后端页面交互交付

2026-10-07，任务 [WAN-245](https://linear.app/wansss/issue/WAN-245)，子需求 WAN-246～248。分支 `codex/wan-245-v3-page-interactions`，起点 `97cfb3887d50acd63a03c2813d71f3d413ce04bf`。范围与用户确认见[计划](../plans/2026-10-07-v3-page-interactions.md)，接口细节见[契约 v1.1](../api/v3-contract.md#v11-页面交互接口2026-10-07)。提交及远端回读由对应 Linear 交付记录登记；尚未合并或部署。

分项提交：[WAN-246 / 7c32e7b](https://github.com/destinywans/aiops-canvas/commit/7c32e7b)、[WAN-247 / e9541ac](https://github.com/destinywans/aiops-canvas/commit/e9541ac)；WAN-248 与本记录同提交。每个分项均从该提交暂存源码生成对应的机器契约，并校验源声明操作集合一致，依次 433 / 442 / 460，不提前提交未来端点。

## 交付行为

32 个新端点已接线，另加本人头像内容 GET。27 项具有持久化或真实读取；Webhook 创建、联系方式验证/解绑、完整通知模板与值班表五项完成鉴权/环境/拒绝边界，按用户确认明确 503，业务来源仍待接。原 19 项复用，原 166 项的实现/产品验收状态保持。

- 连接器配置 CAS、停用与启用复核；MCP 停用及当前候选忽略；令牌原子轮换、一次明文和旧期限上限。
- Agent 工具独立目录、真实运行引用数据集、人工标注、Prompt 历史；记忆编辑仍绑定同源精确摘录、冲突/CAS，保存后的 TTL 实际用于主视图和变更入口。
- SLO 配置 CAS、团队共享查询文本；事件参与人、模板版本与已启动快照隔离；本地维护计划、对象指派与持久 Inbox 通知、真实发送台账；本人私有头像。
- 时间、来源和多值筛选进入真实 SQL/上游查询，计数/排序在分页前完成。Jaeger、VictoriaMetrics、复盘 cause 没有 producer 时 503；日志超配置的 max-window/lookback 返回 503，不缩窗。

## 当前验证证据

全部 PG 证据来自专用临时容器和随机 schema，未改写现有业务库或 testlab。受控模型/遥测 HTTP stub 与隔离 PG 是本地协议/集成证据，不能冒充真实来源或冻结前端验收。

| 检查 | 结果与证据边界 |
| --- | --- |
| OpenAPI | 374 paths / 460 operations / 525 schemas；源声明、引用检查通过，32 新接口加 1 个内容辅助 GET |
| 冻结契约 | 165/165 Mock 路由匹配，含 SSE 共 166；v1.1 单计 51 行：原 19 + 新 27 已实现、5 业务来源待接；route matrix/readiness 通过 |
| 集成生命周期 PG/race | connector、MCP、token 定向通过；403/409 各一条净化失败审计、不保存请求 body |
| Agent/memory PG/race | 最终模块集分别 41/16 tests 与 subtests 通过，0 fail/skip；外层审计接入后再跑新 HTTP 成功链通过 |
| 查询 PG | services/traces/canvas/admin/postmortems/knowledge/observe 整包通过；audit/conversation 修正夹具/编译时编辑冲突后定向通过；新增 facets、home 15m/24h/30d 实际 SQL/边界、日志真实 1h 和超限无 fallback 通过 |
| 正式启动组装 PG/race | `TestConfiguredV3PageInteractions` 六个子例通过、0 skip：参与人及审计回滚、模板 CAS/启动快照、本地维护与站内指派、权限拒绝环境级审计回读、可信来源 503、头像解码/私有无头读取/替换/删除/回滚；模型调用计数为 0 |
| 事件并发 PG/race | 三项回归通过、0 skip：满额去重、旧告警别名负责人一致、对象锁等待中的改派与 A→B→A 通知 epoch |
| 公共库 | httpapi/v3pagequery 定向包通过；共享审计最终版本下 Agent/memory/SLO/team trace views 新 HTTP 用例通过 |
| 独立源码审查 | 四个 P2 与外层审计环境丢失问题已修复并复查；审查范围内无确认未解决 P0/P1/P2，审查人未执行测试 |

原始本地日志保留在忽略目录 `.local/v11-backend-20261007/`：`root-page-fourth.jsonl`、`events-p2-regressions.jsonl`、`agent-memory-final-pg-race.jsonl`、`final-shared-audit-pg-race.jsonl`、`integrations-final-gates-race.log` 等。它们是本机证据，不提交凭据或日志。

首失败保留：并行首次迁移使扩展落入可清理 schema，改为该临时容器 public 预装 pgcrypto/vector；Agent 既有单测一次 503 未能确认原因，重跑通过，不宣称已定位根因；审计夹具误 UPDATE 不可变表改 INSERT，日志夹具适配真实桶/来源窗口，编译期间编辑 import 的过渡失败修正后复验。组装夹具初期把模板启动 201 写成 200，随后发现模板编辑错误要求 canvas.manage，改为既有 canvas.change；环境审计 DTO 只显示姓名，修正误断言用户 ID。失败原件保留，最终结果单列。

## 剩余边界

迁移 158～161 已在本批隔离 PG 执行，尚未在现有业务库执行。维护计划不自动调度静默，指派不发送外部消息，human_ai 没有新增 AI producer，SLO 设置不证明达标；五个可信业务来源仍待接入。前端 HTTP 对齐、现有部署、真实外部模型/来源/发送、完整链、性能/24h均未验收，原暂停域与全接口 OPEN 状态保持。

原未提交验收文档、登记和候选工具按开始时的内容保留，不混入本批提交。专用临时容器 `7aac5efeb2d100830cc69ee80edbb3070416fe9b768886b5e5027c6ff3ad28d0` 在确认 label=wan-245-v11、残留 schema 仅 public 后已 stop/rm，临时 DSN 文件已删除；未清理既有服务、数据卷或共享缓存。
