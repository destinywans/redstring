# AI-first 纠偏基线 · 2026-09-23

## Context

用户明确了产品定位：AI-first 的 Ops 平台（SRE / DevOps）。进来就是大聊天框，可下任意指令；核心价值是复杂运维——故障诊断、修复报告、Runbook 维护；要能看到记忆、状态、上下文如何被管理；非 AI 的日常查看（指标/日志/Trace/CMDB）只做轻量浏览，深入分析跳 Grafana/Kibana 等专有平台；平台做"编排集成 + AI 串联"，不做大而全；Agent trace 希望接 Phoenix / Langfuse。

上一轮 P1（导航五组、toast、StatusBadge、闭环条）是按旧计划做的"整理现有目录"，并未针对这个定位。本计划是对照代码现状后的纠偏建议，P2 暂停。

## 现状对照（基于代码核查）

| 用户期望 | 代码现状 | 差距 |
|---|---|---|
| 一进来就是大聊天框，任意指令 | 首页有 composer，但提交必须选应用+环境+模型，且一定创建 investigation（`DashboardPage.tsx begin()`、`POST /investigations` 必填 application/environment） | 没有"通用对话"；所有对话被强制成调查 |
| 复杂运维：诊断/报告/Runbook 维护 | 单个 ADK `llmagent "investigator"`（`internal/inference/runtime.go:262`），固定指令、只读工具、工具入参只有一个 `query` 字符串；ES/Tempo 有连接器但不是 Agent 工具；恢复报告是确定性拼装、无 LLM 事故报告；Runbook 只能"新建经验草稿" | 能力单一；报告与 Runbook 维护闭环缺失 |
| 看到 Agent 在干什么 | 聊天只渲染纯文本（`convertMessage` 全转 text part），不渲染 markdown，不展示工具调用/步骤；工具参数与每次模型调用不落库 | AI-first 最直观的缺口 |
| 记忆/状态/上下文可见 | 状态：investigation→branch→revision→run→attempt 设计扎实；上下文：`run_context_manifests` + `ContextInspector` 已有；记忆：只有知识文档 + 分支上下文的只读投影（`internal/memoryview`），token 是字节估算 | 底子好但分散，没有统一"透视"视图；token 不真实 |
| Agent trace（Phoenix/Langfuse） | 完全没有 OTel/LLM tracing，不采集 provider usage | 需新建 |
| 编排集成，不做大而全 | 指标/日志/Trace/拓扑/基础设施/资源图谱都在自研展示（Trace 页 1000 行、资源关系 940 行、基础设施看板多个），全仓零外链（无 Grafana/Kibana URL 配置）；RBAC 角色/权限/资源树、部门、交付（前后端合计 ~7k 行，已暂停）占大量体量 | 方向偏向"自研运维平台"；且没有深链能力 |

## 为什么上一轮精简菜单，以及它的局限

- 当时理由：11 个一级分组按后端模块而非用户任务划分；基础设施 9 个子项平铺；交付已暂停仍占一级；审计是管理员功能。
- 局限：五组仍是"功能目录式"导航，默认用户靠菜单找功能——这与 AI-first（靠对话驱动、菜单是次要入口）相反。P1 中可复用的是：toast 队列、StatusBadge、闭环状态条、审批/恢复侧面板适配；导航分组本身会被下面的新 IA 取代。

## 建议的目标形态

### 1. 信息架构（前端 + UI）
- **首页 = 全屏对话**：居中大输入框；左侧为对话历史（类 ChatGPT/Claude）；输入框下方是来自真实信号的"建议任务"卡（未处理告警、待审批、失败的自动初查），点击即带上下文开对话。应用/环境变成可选的上下文 chip，可由 Agent 推断后请用户确认。
- **一级导航收为**：总览（关注 + AI 入口）｜对话/调查（Canvas）｜事件收件箱（告警 + 待审批 + 自动初查结果）｜知识（Runbook + 记忆）｜资源与观测（CMDB + 轻量观测，带外链）｜集成中心（连接器、模型、IM）｜Agent 运行台（Trace、上下文、记忆、Skills/MCP、自动化策略）｜设置（成员、团队、权限、审计）。
- **Canvas 是复杂运维的核心（2026-09-23 用户更正）**：传统对话在调查中会丢失选择分支；Canvas 上每个节点上下文隔离、互不污染，可父→子→孙逐级派生；后代节点的结论可合并/回写到祖先节点，影响后续判断。对话模式与 Canvas 模式并存、可切换，同一会话两种视图。右侧固定 **Agent 透视** 面板：步骤/工具调用｜上下文｜记忆读写｜状态｜Trace（深链 Phoenix/Langfuse）。
- **统一富消息渲染（两种模式共用一个组件）**：markdown（表格、代码）、列表、图表（指标证据内联成可交互图）、引用角标（证据/Runbook，点开侧看原文）、工具调用卡（名称、参数、耗时、token、结果→证据）、提案卡（直接开审批侧面板）；实时流式更新。
- **视觉**：现代简洁、Linear 风格——中性灰阶 + 单一强调色、细边框少阴影、紧凑排版、键盘优先（⌘K）、明暗主题；可选背景/密度设置。

### 1.5 场景范围（2026-09-23 用户说明）
平台覆盖 SRE + DevOps 全链路，不只调查：日常运维（"看下这个服务状态、P99 多少、要不要扩容"）、咨询建议、基于 Runbook/大模型的知识学习与操作、远期 CI/CD 编排。当前 Demo 以调查为主线，但入口与对话模型必须按"通用运维对话"设计，调查只是其中一种。

### 1.6 企业 SaaS 定位（2026-09-23 用户说明）
- **集成中心是核心模块**：统一登记外部连接器——模型、Prometheus、Elasticsearch、Harbor、Grafana、Kibana、IM（Slack/飞书/钉钉/企业微信）等。Agent 执行指令时经连接器取实时明细数据；平台内展示只是摘要/快照。阶段 3 的类型化工具与阶段 5 的深链模板都挂在集成中心的连接器登记上。
- **首页 = 关注总览 + AI 入口**（不是纯聊天框）：哪些服务/应用异常、哪些待我处理，同屏可直接对 AI 下指令；现有首页（输入框 + 关注/待办卡）作为基础演进。
- **主流程**：告警 → 发起调查 → 人工复核/批准并执行 → 执行记录 → 生成报告。
- **企业能力保留**：认证鉴权、RBAC、团队/部门/人员（邮箱、电话）继续作为平台基础，不冻结。

### 2. 对话模型（后端）
- 新增顶层 **Conversation**：不强制应用/环境；**Investigation 是带作用域的 Conversation**（可从对话"升级"，沿用现有 branch/revision/run/attempt，不重写）。
- 作用域延迟绑定：Agent 通过 `resolve_scope`（查 CMDB/应用）提出作用域，用户确认后才解锁该应用的工具；每次工具读取仍复检 ACL（保留现有安全模型）。
- 工具改为**类型化参数**（不再只有 `query` 字符串），新增：`query_traces`(Tempo)、`search_logs`(ES)、`get_resource`/`list_relations`(CMDB)、`list_alerts`/`get_incident`。

### 3. Agent 能力（ADK）
- 从单 agent 改为 ADK 多 agent：**协调者** + 专家（诊断、报告撰写、Runbook 维护、日常只读查询）。Skills 作为专家/指令注入而不是只拼进用户消息。
- **故障报告**：LLM 起草（时间线来自 events、证据引用、根因、处置、后续项），人工编辑确认，导出 Markdown；保留现有确定性恢复记录作为事实附件。
- **Runbook 维护闭环**：调查结束后 Agent 提出对**已有** Runbook 的修改 diff（像 PR 一样审阅合入），不只新建草稿；标记长期未被召回/与事实冲突的 Runbook。

### 4. 记忆 / 状态 / 上下文 的可见性
- 统一三层记忆并在 UI 中明确命名：**工作记忆**（分支上下文 + 摘要，已有）、**情景记忆**（历史对话/调查/报告，新增检索）、**语义记忆**（Runbook/知识，已有 pgvector）。
- 记录**记忆读写事件**：本轮召回了哪些文档（分数、来源）、本对话产生了哪些草稿/修改。
- 上下文透视升级：按组成拆分（系统指令/Skills/证据/历史/摘要/工具 schema）的 token 条，用 provider 返回的真实 usage 替换字节估算。
- 状态：对话→分支→版本→run→attempt 的时间线视图（数据已全在 PG）。

### 5. Agent Trace
- 在 `internal/inference` 用 **OpenTelemetry GenAI 语义约定** 打点：run / 模型调用 / 工具调用 span，含 usage、模型、耗时、错误；OTLP 导出，**厂商中立**。
- 默认对接 **Phoenix**（单容器、原生 OTLP/OpenInference，适合自托管演示）；Langfuse 也接受 OTLP，可后切。先确认 ADK Go 自带 telemetry 能否复用。
- 平台内只展示摘要（步骤、token、耗时），"在 Phoenix 中打开"深链看完整 trace——与"编排不重造"一致。

### 6. 编排集成而非重造
- 连接器配置增加 **UI 深链模板**（Grafana dashboard / Explore、Kibana Discover、Jaeger/Tempo UI），支持 `${app} ${env} ${from} ${to} ${trace_id}` 变量；证据卡、观测页、资源详情都带"在 X 中打开"。
- 观测页收敛为"一眼看"：黄金信号卡、最近日志样本、慢/错 trace 列表 + 外链；停止扩展自研拓扑/大看板。
- 暂缓投入：资源关系图谱的继续扩展；删除前端无引用代码（~2.5k 行，交付子树除外）。RBAC/团队/人员属企业基础能力，保留。
- **交付（CI/CD）不删除，冻结待复用（2026-09-23 用户说明）**：远期由对话驱动"构建 / 部署 / 发布到某环境 / 灰度规则"，交付模块作为 Agent 的写动作后端，仍走 提案 → 人工批准 → 独立执行器。

### 7. 后端技术债（顺手纠正）
- operations/knowledge/actions/deployment/delivery/aiextensions 在 Go 启动时建表，不在 `migrations/`——与项目约定不一致，统一迁移。
- pgvector 目前精确扫描无索引：先加 HNSW；向量检索抽象为接口，为 Qdrant 预留。
- SSE 以 500ms 轮询 events 表：可改 PG `LISTEN/NOTIFY` 唤醒（保留表做回放）。
- `/investigations/:id` 与 `/investigations/*` 重复路由等小问题。

## 分阶段路线（每阶段可独立演示）

1. **Agent 可见性地基**：工具调用步骤落库 + provider usage；OTel span + Phoenix；SSE 推送步骤事件；前端统一富消息组件（markdown + 工具调用卡 + 引用），Canvas 节点卡与对话侧栏共用；Agent 透视面板（上下文/状态两栏用现有数据）。
   - 后续补：祖先←后代的"回写"交互在 Canvas 上直观化（现有 merge-preview/merge 支持任意源→目标，需验证跨代并做成拖拽/一键回写）。
2. **AI-first 入口**：Conversation 模型、延迟作用域、首页全屏对话 + 历史栏 + 建议任务卡；新 IA 导航。
3. **能力扩展**：类型化工具（ES/Tempo/CMDB/告警）、多 agent、故障报告生成、Runbook 修改提案。
4. **记忆可见性**：三层记忆命名、召回/写入事件、情景记忆检索、记忆页改版。
5. **编排外链与瘦身**：深链模板、观测页收敛、冻结/删除清单执行。
6. **技术债**：迁移统一、HNSW、向量接口、LISTEN/NOTIFY。

## 关键文件（实施时）
- 后端：`internal/inference/runtime.go`、`provider.go`、`context.go`；`internal/httpapi/worker.go`、`server.go`；`internal/store/runs.go`、`investigations.go`；`cmd/aiops/telemetry.go`、`platform.go`；`internal/knowledge/*`；`internal/actions/review.go`；`migrations/`；`internal/apicontract/openapi.json` + `scripts/build-openapi.py`。
- 前端：`web/src/features/workspace/DashboardPage.tsx`、`HomeComposer.tsx`；`features/investigation/InvestigationPage.tsx`（拆分）、`ContextInspector.tsx`、`InvestigationLoop.tsx`；`features/memory/MemoryPage.tsx`；`lib/navigation.ts`、`App.tsx`。
- 可复用：`InvestigationLoop` + `features/actions/routes.tsx`（审批侧面板）、`components/ui/status-badge.tsx`、`lib/toast.ts`、`ContextInspector`、`memoryview` API、`run_context_manifests`、`knowledge.SearchScoped`。

## 验证
- 每阶段：受影响 Go 包测试 + `pnpm --dir web build`；OpenAPI 生成并校验；隔离 PG 跑迁移。
- 端到端：在 `aiops-test` 实验集群（显式 kubeconfig）发起一次"无作用域对话→Agent 推断应用→确认→诊断→提案→审批→180 秒验证→生成报告→Runbook 修改提案"，浏览器核对工具调用卡、Agent 透视、Phoenix trace 深链。

## 待用户确认
- 对话与调查的关系（建议：Conversation 顶层，调查为带作用域的对话）。
- Trace 后端先接 Phoenix 还是 Langfuse（建议 OTel 中立 + 先 Phoenix）。
- ~~画布定位~~ 已定：Canvas 为核心模式，与对话模式并存。
- 已定：交付冻结保留；RBAC/团队/人员保留；集成中心为核心模块；首页为关注总览 + AI 入口。

## 阶段 1 进度（2026-09-23，分支 `codex/ai-first-agent-visibility`，已提交未推送）

- 后端 `26106b5`：`run_steps`（迁移 022）记录每次模型调用与只读工具调用；provider 真实 usage（OpenAI `include_usage`、Claude `message_start/message_delta`）；ADK telemetry + `aiops.attempt` 父 span，OTLP 导出；`GET /runs/{id}/steps` 与 `AIOPS_TRACE_UI_URL`；助手消息带 `run_id`。契约 +193 行，`check-contract.py` 通过。
- 前端 `15301f0`：`features/conversation` 统一富消息（GFM markdown、步骤卡、引用、证据内联图表），Canvas 节点与对话侧栏共用；SSE `attempt.step` 实时更新；侧栏新增 Agent 页签（状态链、合计、步骤、上下文、Trace 外链）。
- 验证：`go test ./internal/inference`、store 新测试（隔离 PG）通过；`pnpm --dir web build` 通过；preview 浅/深色核对。真实端到端：本地 Ollama `qwen3.6` + aiops-test Prometheus（只读）+ Jaeger，模型调用 `query_metrics`，步骤、usage（493→207、2171→536）、证据关联、`run_id` 与 Jaeger 中 span 嵌套均正确。
- 未验证：Phoenix/Langfuse 对 GenAI 语义约定的具体展示效果（用 Jaeger 验证了 OTLP 导出）；登录态下浏览器里的实时步骤流（需用户登录后查看）。
- 既有问题（基线提交同样失败，非本次引入）：`internal/store` 的 `TestMemberLifecycleAndImportPostgres`、`TestPlatformFoundationPostgres`，`internal/httpapi` 的 3 个 ACL 测试，均为 "application permission denied"。根因：`authorizeApplicationRBAC` 要求应用关联的基础设施资源存在且全部可见，未关联资源的应用（如 `demo`）对管理员也不可访问。
