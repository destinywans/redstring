# v3 前端 API 契约（Web ⇄ 后端）

- 状态：**前端已按本契约用 Mock 实现，后端待实现**（2026-09-24；同日 v4 增补：模型与推理强度、附件、通用画布、各"新建"流程、链路 v2）。
- 类型真相源：`web/src/v3/api/types.ts`。本文件只描述路径、参数和语义，字段以该文件为准。
- Mock 实现：`web/src/v3/mock/domains/*.ts`，每个文件对应下面一节；`VITE_API_MODE=http` 时前端直接请求真实后端。
- 与既有 `docs/api-contract.md` / `internal/apicontract/openapi.json` 的关系：v3 为新 UI 所需的读模型。后端落地时优先复用既有资源（调查、分支、提案、执行、Runbook、连接器）；能直接对齐的路径就对齐，对不齐的在 OpenAPI 中新增，再同步 `scripts/build-openapi.py`。本文件的"状态"列在后端实现后改为 `implemented`。

## 通用约定

| 项 | 约定 |
| --- | --- |
| 基址 | `/api/v1`，同源 Cookie 会话（`credentials: same-origin`） |
| 格式 | 请求和响应都是 JSON；`204` 表示无正文 |
| 语言 | 请求头 `Accept-Language: zh-CN \| en-US`。**所有面向用户的文案由后端按语言返回单一字符串**（Mock 用 `l(zh, en)` 存双语，由 Mock 路由按语言展开）。标识符、服务名、查询语句等不翻译 |
| 时间 | 列表中的展示型时间（如 `14:03`、`昨天`）目前由后端格式化后返回；需要计算的时间字段使用 RFC 3339 |
| 分页 | `?page=1&page_size=50`，响应 `Page<T> = { items, total, page, page_size }`；目前只有对话列表使用 |
| 错误 | 非 2xx 返回 `{ "error": { "code": "not_found", "message": "…" } }`，前端抛出 `ApiError(status, code, message)` |
| 过滤 | 列表过滤用查询参数：`q`（搜索）、`filter` / `category` / `kind` 等枚举；数组参数重复键（`?k=a&k=b`） |
| 写操作 | 模型只读取证。任何对外部系统的写动作都只生成**提案**，人工批准后由独立执行器执行；本契约中的 POST/PUT/PATCH 只修改平台自身配置或状态 |
| 审计 | 所有写请求和 Agent 工具调用都写入审计（`GET /audit`） |

### 流式对话

`POST /conversations/{id}/messages` 提交用户消息，返回 `{ message, reply_id }`；随后 `GET /conversations/{id}/events`（SSE）推送 `StreamEvent`：

| 事件 | 含义 |
| --- | --- |
| `message.started` | 助手消息开始，携带空 `parts` |
| `part.added` | 新增一个富消息部件（`tool_calls`、`text`、`chart`、`table`、`citations`、`proposal`…，见 `Part`） |
| `text.delta` | 向第 `index` 个 `text` 部件追加 `delta` |
| `message.completed` | 结束，前端关闭 EventSource |

**按轮选择模型（v4）**：请求体可带 `model`（`provider:model`，来自 `GET /models`）与 `reasoning`（该模型声明的档位 id）；缺省时用 `GET /models` 的 `default`。一轮只用一个模型，回复进行中切换只影响下一轮；`message.completed` 的 `run` 记录 `model_id`、`model`、`reasoning`、`reasoning_label`、`brand`。附件先经 `POST /uploads` 上传，再以 `attachments: Upload[]` 随消息提交；附件一律作为数据，不执行其中的指令。

画布节点对话复用同一套部件与事件（`POST /investigations/{id}/nodes/{node}/messages`、`POST /canvases/{id}/nodes/{node}/messages`）。

## 身份与全局（`identity.ts`、`home.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/me` | | `Me` | mock |
| PATCH | `/me` | `Partial<Me>` | `Me` | mock |
| PATCH | `/me/preferences` | `{ theme?, locale?, density?, reduce_motion? }` | 偏好 | mock |
| GET | `/me/watched-services` | | `{ items: WatchedService[] }` | mock |
| GET | `/search` | `q` | ⌘K 搜索结果 | mock |
| GET | `/mentions` | `q`, `kind` | `@` 提及候选 | mock |
| GET | `/home` | | `HomeSummary`（v4 增加 `briefing`：带引用的摘要、生成时间、当前值班） | mock |
| GET | `/mentions/{kind}/{id}/preview` | | `MentionPreview`（`@` 面板右侧预览） | mock |
| GET | `/commands` | | `{ items: CommandItem[] }`（`/` 面板：命令、Skills、Runbook、模板） | mock |
| GET | `/models` | | `ModelCatalog`：当前用户可用的模型、接口类型、推理强度档位与默认值 | mock |
| GET | `/models/admin` | | `ModelAdminView`（集成中心 › 模型） | mock |
| POST | `/uploads` | multipart `file` + `kind`；链接与折叠粘贴用 JSON `{ kind, name, url \| text }` | `Upload`；单个 ≤ 20 MB，超出返回 413 | mock |

主题、语言、密度、减少动效保存在浏览器本地（`aiops-v3-preferences`），并尽力同步到 `PATCH /me/preferences`。

## 对话、画布与模板（`conversations.ts`、`canvases.ts`、`canvas.ts`、`templates.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/conversations` | `filter`, `q`, 分页 | `ConversationList` | mock |
| GET | `/conversations/{id}` | | `Conversation` | mock |
| POST | `/conversations` | `{ text, context?, model? }` | `{ id }` | mock |
| POST | `/conversations/{id}/messages` | `{ text, model?, reasoning?, attachments? }` | `{ message, reply_id }` + SSE | mock |
| GET | `/canvases` | `kind`, `q`, 分页 | `{ items: CanvasSummary[], counts }`；`kind`：investigation / plan / review / analysis / custom | mock |
| POST | `/canvases` | `{ title, kind, template_id, goal, services, ai_draft }` | `{ id, href }`；调查类返回 `INV-*` | mock |
| GET | `/canvases/{id}` | | `CanvasDoc`（非调查类画布：通用节点块、边、产物、各节点对话） | mock |
| POST | `/canvases/{id}/nodes/{node}/messages` | `{ text, model?, reasoning? }` | `{ reply }` | mock |
| GET | `/canvas-templates` | | `{ items: CanvasTemplateOption[] }` | mock |
| POST | `/canvas-templates` | `{ from_canvas, name, keep }`（只保存结构与只读步骤） | 模板 | mock |
| GET | `/investigations/{id}/canvas` | | `CanvasGraph` | mock |
| POST | `/investigations/{id}/branches` | `{ parent, title }` | `CanvasNode` | mock |
| PATCH | `/investigations/{id}/nodes/{node}` | 位置、折叠、标题 | `CanvasNode` | mock |
| POST | `/investigations/{id}/nodes/{node}/messages` | `{ text }` | `{ summary }` | mock |
| GET | `/investigations/{id}/writeback-preview` | `node` | 回写预览 | mock |
| POST | `/investigations/{id}/writeback` | `{ node, target, items }` | `{ version }` | mock |
| GET | `/investigation-templates` | | 模板列表 | mock |
| POST | `/investigation-templates/{id}/start` | `{ inputs }` | `{ investigation_id }` | mock |

## 事件与告警（`events.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/inbox` | `filter` | 收件箱条目 | mock |
| GET | `/alerts/{id}` | | `AlertDetail` | mock |
| POST | `/alerts/{id}/claim` | | 更新后的告警 | mock |
| GET | `/alert-groups` | `q`, `severity`, `env` | 告警分组 | mock |
| GET | `/alert-groups/{id}/policy` | | `AlertGroupPolicy` | mock |
| POST | `/alert-groups/bulk` | `{ ids, action: claim\|silence\|investigate }` | 结果 | mock |
| GET | `/services/{id}/peek` · `/people/{name}/peek` | | `ServicePeek` · `PersonPeek`（悬浮卡） | mock |
| GET | `/incidents` · `/incidents/{id}` | | 事件列表 · `Incident` | mock |
| POST | `/incidents/{id}/timeline` | `{ text }` | 时间线条目 | mock |
| GET | `/alert-rules` · `/alert-rules/{id}` | | 规则列表 · `AlertRule` | mock |
| POST | `/alert-rules/{id}/dry-run` | `{ expr, for }` | `{ would_fire, windows }` | mock |
| PUT | `/alert-rules/{id}` | `AlertRule` | `AlertRule` | mock |
| GET | `/notification-routes` | | `{ root: RouteNode }` | mock |
| POST | `/notification-routes/test` | 样例告警 | 命中路径 | mock |
| GET | `/silences` · `/inhibitions` | | 静默 · `{ items: Inhibition[] }` | mock |
| POST | `/silences/preview` | 匹配器 | `{ affects, sample }` | mock |
| POST | `/silences` · DELETE `/silences/{id}` | 匹配器 + 时长 | `{ id }` | mock |
| GET | `/proposals/{id}` | | `Proposal` | mock |
| POST | `/proposals/{id}/approve` · `/reject` | `{ comment? }`（批准需通行密钥二次验证） | `{ execution_id }` | mock |
| POST | `/proposals/{id}/comments` | `{ text }` | 评论 | mock |
| GET | `/executions/{id}` | | `Execution` | mock |
| POST | `/executions/{id}/report/publish` | | 发布结果 | mock |

## 知识与记忆（`knowledge.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/runbooks` · `/runbooks/{id}` | `q`, `service` | 列表 · `Runbook` | mock |
| POST | `/runbooks/{id}/dry-run` | 当前草稿 | 演练结果 | mock |
| POST | `/runbooks/{id}/submit` | 草稿 | 提交审核 | mock |
| GET | `/postmortems` | | `PostmortemSummary` | mock |
| GET | `/memory` | | `MemoryView` | mock |
| POST | `/memory/facts/{id}/{action}` | `action: confirm\|reject\|expire` | 更新后的事实 | mock |
| POST | `/memory/conflicts/resolve` | `{ keep, drop }` | 结果 | mock |
| GET | `/knowledge-sources` | | 知识来源 | mock |
| POST | `/knowledge/search` | `{ q }` | `{ hits: RetrievalHit[] }` | mock |

## 资源与观测（`observe.ts`）

只读聚合；遥测数据按需从连接器查询，不落平台。

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/services` | `q`, `tier`, `team`, `status` | `ServiceCatalog` | mock |
| GET | `/services/{id}` | | `ServiceDetail` | mock |
| GET | `/services/{id}/resilience` | | `Resilience`（灰度 / 熔断 / 限流 / 开关 / HPA，只读） | mock |
| POST | `/services/{id}/resilience/proposals` | `{ kind, params }` | `{ proposal_id }`（只生成提案或合并请求） | mock |
| GET | `/topology` | `cluster`, `around`, `depth`, `group_by` | `Topology` | mock |
| GET | `/metrics/dashboard` | `service`, `range`, `align` | `MetricsDashboard` | mock |
| POST | `/metrics/ask` | `{ view, question }` | `{ conversation_id }` | mock |
| GET | `/traces` | `scope: app\|agent`, `view`, `q`（TraceQL / 过滤表达式）, `service` | `TraceList`：分面计数、保存的视图、按状态直方图、耗时散点、表格 | mock |
| GET | `/traces/{id}` | | `TraceView`：Span（关键路径、属性、事件、日志、关联）、火焰图说明、服务调用图 | mock |
| GET | `/services/discover` | `cluster` | 只读发现未登记的工作负载与负责人、系统映射线索 | mock |
| POST | `/services` | `{ items: [{ id, namespace, tier, owner, slo, p99 }] }` | `{ created }` | mock |
| GET | `/logs` | `q`, `levels` | `LogSearch`（含 AI 聚类的日志模式） | mock |
| GET | `/changes` | `range`, `sources` | `ChangesView`（CI/CD 与 GitOps 事件，只读） | mock |

## v4 新建流程（`create.ts`）

所有"新建"只修改平台自身配置；带"AI 起草"的接口只返回草稿，由人确认后再提交。

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/incidents/draft` | `alerts` | 由告警预填的事件草稿 | mock |
| POST | `/incidents` | `{ title, severity, impact, env, services, alert_ids, commander, war_room, notify, status_page, open_canvas }` | `{ id, investigation_id? }`；作战群、通知、状态页属外部动作，后端按各自审批规则执行 | mock |
| POST | `/alert-rules/generate` | `{ text }` | PromQL 与阈值草稿 | mock |
| POST | `/alert-rules/preview` | `{ query, op, threshold, for_minutes, range }` | 校验、曲线、触发区间、7 天回测、会命中的路由、相似规则 | mock |
| POST | `/alert-rules` | `{ …, mode: draft\|dry_run\|enabled }` | `{ id, status }` | mock |
| POST | `/alert-rules/import` | `{ yaml }` | 解析结果（新建 / 重复），导入后为草稿 | mock |
| POST | `/notification-routes/test` · `/notification-routes` | 匹配条件、接收方、分组、时段、升级 | 测试结果 · `{ id }` | mock |
| POST | `/inhibitions/preview` · `/inhibitions` | `{ source, target, equal }` | 过去 7 天会抑制的数量与样例 · `{ id }`（P1 永不抑制） | mock |
| GET | `/runbooks/draft` | `source: template\|postmortem\|conversation\|import`, `from` | Runbook 草稿（写操作步骤标记 `approval`） | mock |
| POST | `/runbooks` | `{ title, source, from, steps, status: draft\|review }` | `{ id }`；复核通过前 Agent 不使用 | mock |
| GET | `/postmortems/candidates` · `/postmortems/draft` | `incident` | 可写复盘的事件 · 复盘草稿 | mock |
| POST | `/postmortems` | `{ incident_id, sections, actions, status }` | `{ id }` | mock |
| POST | `/knowledge-sources/test` · `/knowledge-sources` | `{ type, url, secret_ref: "vault:…", spaces, schedule, chunk, inherit_acl }` | 连接结果 · `{ id, status }`；只接受 Vault 引用 | mock |

## Agent 链路（`traces.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/agent-runs/{id}/trace` | | `AgentTrace`：汇总、会话各轮模型、Span 树（agent / chain / llm / tool / retriever / guardrail），LLM 消息、参数、token 构成、评分，工具参数与结果 | mock |

## 集成中心（`integrations.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/connectors` | `category`, `q` | `ConnectorList` | mock |
| GET | `/connectors/{id}` | | `ConnectorDetail` | mock |
| POST | `/connectors/{id}/test` | | `{ ok, latency_ms }` | mock |
| PATCH | `/connectors/{id}` | 如 `{ tools: [{ name, enabled }] }` | 更新结果 | mock |
| POST | `/connectors` | `{ type, url, auth, tools, webhook }`（凭据只传 Vault 引用） | `{ id }` | mock |
| GET | `/connector-types/{type}` | | `ConnectorType`（认证方式、权限范围、只读工具） | mock |
| GET | `/mcp-servers` · `/mcp-servers/{id}` | | `McpList` · `McpDetail` | mock |
| PATCH | `/mcp-servers/{id}/tools/{name}` | `{ enabled }`（写入工具开启后仍只能以提案使用） | 结果 | mock |
| POST | `/mcp-servers/{id}/review` | 审核新版本发现的工具 | 结果 | mock |
| GET | `/ingest/pull/{id}` | | `PullDraft`（请求模板、样例、字段映射、预览） | mock |
| POST | `/ingest/pull/{id}/test` | 映射 | `{ ok, rows, dropped }` | mock |
| GET | `/ingest/push` | | `PushView`（事件类型、接口说明、投递记录、配套栈） | mock |
| POST | `/ingest/push/test` | `{ type }` | `{ code }` | mock |
| GET | `/service-mappings` | | `ServiceMapping` | mock |
| POST | `/service-mappings/accept` | `{ cells: ["service:system"] }` | `{ accepted }` | mock |
| POST | `/service-mappings/discover` | | `{ suggestions }` | mock |
| PUT | `/service-mappings` | 完整映射 | 结果 | mock |
| GET | `/coverage` | `app` | `CoverageView`（覆盖矩阵、对 Agent 的影响、修复建议） | mock |
| GET | `/webhooks` | | `WebhookView`（入站、令牌、对外 MCP、出站） | mock |
| POST | `/webhooks/{id}/redeliver` | | 结果 | mock |
| POST | `/api-tokens` | `{ name, scopes, ttl }` | `{ id, secret_shown_once }` | mock |

Push 事件接入（供外部系统调用，不是前端调用）：`POST /api/v1/events/{alert|change|incident|resource|oncall}`，请求头 `Authorization: Bearer`、`Idempotency-Key`、`X-Signature` + `X-Timestamp`；返回 `202 / 409 / 422 / 401`。

## Agent 运行台（`agents.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/agents/lifecycle` | | `Lifecycle` | mock |
| GET | `/agents/{id}` | | `AgentDetail` | mock |
| POST | `/agents/{id}/rollout` | `{ to, percent, override }`（`override` 表示管理员放行未通过的门禁，写审计） | 结果 | mock |
| POST | `/agents/{id}/rollback` | | 结果 | mock |
| PATCH | `/agents/{id}/level` | `{ level: L0..L3 }`（受环境上限约束） | 结果 | mock |
| GET | `/prompts` · `/prompts/{id}` | | `{ items: PromptItem[] }` · `PromptDetail` | mock |
| POST | `/prompts/{id}/trial` | `{ case, a, b }` | `PromptTrial[]` | mock |
| POST | `/prompts/{id}/versions` | `{ text }` | `{ version }` | mock |
| GET | `/agent-tools` | `kind: all\|read\|write` | `ToolsView` | mock |
| GET | `/evals/{id}` · POST `/evals` | | `EvalExperiment` · `{ id }` | mock |
| GET | `/eval-cases/{id}` | | `EvalCase` | mock |
| POST | `/eval-cases/{id}/labels` | 人工评分 | `{ next }` | mock |
| GET | `/agent-runs` | `filter: all\|failed\|blocked\|negative` | `RunsView` | mock |
| GET | `/agent-runs/{id}` | | `RunDetail`（执行时间线、上下文组成、记忆） | mock |
| POST | `/agent-runs/{id}/feedback` | `{ vote: up\|down }` | 结果 | mock |
| GET | `/guardrails` | `filter: all\|blocked\|masked` | `GuardrailsView` | mock |
| PATCH | `/guardrails/{id}` | `{ enabled }`（强制规则不可关闭） | 结果 | mock |

## 设置（`settings.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET · PUT | `/me/profile` | `ProfileView` | `ProfileView` | mock |
| DELETE | `/me/sessions/{id}` | | 204 | mock |
| GET | `/members` | `filter: all\|admin\|no_mfa\|disabled`, `q` | `MembersView` | mock |
| POST | `/members/invitations` | `{ email, role? }` | 结果 | mock |
| POST | `/members/sync` | | `{ added, updated }` | mock |
| POST | `/members/mfa-reminders` | | `{ sent }` | mock |
| GET | `/roles` · `/roles/{id}` | | `{ items: Role[] }` · `RoleDetail` | mock |
| PUT | `/roles/{id}` | `{ matrix }`（机器身份的"批准"列必须拒绝） | 结果 | mock |
| GET · PUT | `/security` | `SecurityView` | `SecurityView` | mock |
| GET · PUT | `/me/notification-settings` | `{ quiet: { enabled, from, to }, digest: off\|daily\|weekly, subscriptions }` | 同左 | mock |
| GET · PUT | `/me/ai-preferences` | `{ default: ModelChoice, canvas_inherit, show_reasoning, auto_context, memory, budget_warn_tokens }`；`GET /models` 的 `default` 取自这里 | 同左 | mock |
| GET · PUT | `/workspace` | `{ name, slug, timezone, locale, week_start, retention_days, data_region, brand }`（仅管理员可改，写入审计） | 同左 | mock |
| GET | `/audit` | `filter: all\|user\|agent\|system`, `q` | `AuditView` | mock |
| GET | `/audit/{id}` | | `AuditDetail`（含哈希链校验结果） | mock |
| POST | `/audit/export` | 过滤条件 | `{ url }` | mock |

## 后端实现提示

- 后端实现一个端点后，把状态改成 `implemented`，并删除或保留对应的 Mock（保留时仅用于开发）。
- 字段有分歧时先改 `types.ts`，再让 Mock 和后端同时跟进；前端 `npx tsc -b` 会暴露所有受影响的页面。
- 推理强度（v4）：后端目前支持 OpenAI 兼容 Chat Completions（含 Ollama）与 Claude Messages，尚无 Responses API 与强度透传。`GET /models` 的档位由后端按模型声明返回；映射为 `reasoning.effort`（Responses）、`reasoning_effort`（Chat Completions）、Claude 的 thinking 参数或 Ollama 的 `think` 开关。
- 示意数据只在 Mock 模式下出现，页面顶部有"示意数据"标记；真实模式下缺失的能力应返回空列表或明确的"待接入"状态，不能用示意数据代替。
