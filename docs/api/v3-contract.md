# v3 前端 API 契约（Web ⇄ 后端）

- 状态：**契约冻结 v1（2026-09-28）**。前端已按本契约用 Mock 实现；后端按接口逐项迁移，`mock` 仍表示尚未达到后端完成标准，即使已有部分处理逻辑。历史：2026-09-24 初版；v4 增补模型与推理强度、附件、通用画布、各"新建"流程、链路 v2；2026-09-28 增补实例与环境。
- 表格每行只写一个方法和一个路径。`scripts/check-v3-contract.py` 会核对本文件和 Mock 路由是否一致，改契约后运行它。
- 冻结后的修改流程：先改本文件和 `types.ts`，再改 Mock 与后端；破坏性修改（删字段、改语义）在「变更记录」里写明。
- 类型真相源：`web/src/v3/api/types.ts`。本文件只描述路径、参数和语义，字段以该文件为准。
- Mock 实现：`web/src/v3/mock/domains/*.ts`，每个文件对应下面一节；`VITE_API_MODE=http` 时前端直接请求真实后端。
- 与既有 `docs/api-contract.md` / `internal/apicontract/openapi.json` 的关系：v3 为新 UI 所需的读模型。后端落地时优先复用既有资源（调查、分支、提案、执行、Runbook、连接器）；能直接对齐的路径就对齐，对不齐的在 OpenAPI 中新增，再同步 `scripts/build-openapi.py`。本文件的"状态"列在后端实现后改为 `implemented`。
- `scripts/check-v3-contract.py` 核对的 165 条 Mock 路由，加上前端模拟而无 Mock 路由的 SSE `GET /conversations/{id}/events`，共 166 条。`implemented` 只表示后端真实处理、范围鉴权、契约和定向检查具备；不表示外部系统已实连或前端 HTTP 模式已验收。阶段进展与未完成项见 [后端实施记录](../plans/2026-09-28-v3-backend-implementation.md)。

## 通用约定

可选的企业 OIDC 登录属于 **166 项 V3 页面契约之外的认证辅助接口**：`GET /auth/oidc/start` 发起浏览器授权码登录，`GET /auth/oidc/callback` 接收 `state`、`code` 并在验证后设置现有本地会话 Cookie。两者只在完整配置 IdP 时注册；未配置为 404。服务端要求浏览器绑定的一次性状态、nonce、PKCE S256，以及签名、issuer、audience、过期时间校验。登录只匹配由平台管理员显式绑定的服务端 issuer 与 IdP subject；绑定目标必须是已启用的现有人类成员，不按邮箱匹配、不自动建号、不改变成员来源。登录后仍以本地角色和环境授权为准，不接受 IdP 声明的角色或环境。回调使用 SameSite=Lax 本地会话 Cookie；禁用成员和解绑会撤销本地会话，但 IdP 侧会话、签发的令牌及已进行中的请求不能由此撤销。

绑定管理只在配置 OIDC 时注册，由平台管理员且具有全局 `members.manage` 的本地会话调用；写请求沿用 V3 环境规则，`all` 返回 `409 env_view_only`。`GET /auth/oidc/bindings/{user}` 查看当前 issuer 下的 subject；`POST /auth/oidc/bindings` 以 `{user_id,subject}` 显式绑定，重复 subject 或用户返回 `409`；`DELETE /auth/oidc/bindings/{user}` 解绑。客户端不能选择 issuer；绑定与解绑写审计并撤销目标用户现有本地会话。绑定已有成员需要先通过现有受控成员流程建立账号，新成员自助开通不属于这组辅助接口。

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
| 环境 | 请求头 `X-AIOps-Environment: <环境 id> \| all`，取右上角的当前环境；列表、AI 取证范围和执行目标都按它过滤。`all` 只允许在非生产实例中使用，且只能查看，写请求要返回 `409 env_view_only`。生产实例只有 `prod` 一个环境，可以不带这个请求头 |

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
| PATCH | `/me/preferences` | `{ theme?, locale?, density?, reduce_motion? }` | 偏好 | implemented |
| GET | `/me/watched-services` | | `{ items: WatchedService[] }` | mock |
| GET | `/search` | `q` | ⌘K 搜索结果 | mock |
| GET | `/mentions` | `q`, `kind` | `@` 提及候选 | mock |
| GET | `/home` | | `HomeSummary`（v4 增加 `briefing`：带引用的摘要、生成时间、当前值班） | mock |
| GET | `/mentions/{kind}/{id}/preview` | | `MentionPreview`（`@` 面板右侧预览） | mock |
| GET | `/commands` | | `{ items: CommandItem[] }`（`/` 面板：命令、Skills、Runbook、模板） | mock |
| GET | `/models` | | `ModelCatalog`：当前用户可用的模型、接口类型、推理强度档位与默认值 | implemented |
| GET | `/models/admin` | | `ModelAdminView`（集成中心 › 模型） | mock |
| POST | `/uploads` | multipart `file` + `kind`；链接与折叠粘贴用 JSON `{ kind, name, url \| text }` | `Upload`；单个 ≤ 20 MB，超出返回 413 | implemented |

### 实例与环境（`instance.ts`）

**一个平台实例只对应一个隔离边界。**生产平台独立部署：独立域名、数据库和凭据，界面上固定显示“生产”，不能切换。非生产平台管理开发、测试、预发和需求验证环境，可以切换，也可以选「全部非生产环境」查看。

- **跨环境读取**只读且单向：生产可以通过「低环境只读来源」读取非生产环境做对比，凭据存放在生产的 Vault；非生产平台不能访问生产。
- **配置提升**不在两个平台之间直接调用：非生产导出为配置仓库的合并请求，审核合并后，由生产平台在「配置同步」中导入。执行记录和证据不随之带过去。

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/instance` | | `InstanceInfo`：`tier`（`production` \| `nonprod`）、`name`、`domain`、`environments[]`（id、名称、分组、集群、回收时间）、`default_env`，以及另一个平台的链接 `other` | implemented |
| GET | `/integrations/lower-sources` | | `{ items: LowerSource[] }`，只有生产实例提供；非生产实例返回 404。仅返回服务端显式配置的来源元数据，不代表连接已验证 | implemented |
| GET | `/config-sync` | | `{ direction: "import" \| "export", items: SyncItem[] }`：生产实例列出待导入项，非生产实例列出已导出项 | mock |
| POST | `/config-sync/{id}/import` | | `SyncItem`；只允许生产实例导入已合并的变更 | mock |
| POST | `/promotions` | `{ kind, id }` | `{ mr, path }`：写入配置仓库并发起合并请求；生产实例返回 `409 already_production` | mock |

当前环境保存在浏览器本地（`aiops-v3-env`），并写入地址栏 `?env=`，这样分享链接时打开的是同一个环境。

主题、语言、密度、减少动效保存在浏览器本地（`aiops-v3-preferences`），并尽力同步到 `PATCH /me/preferences`。

## 对话、画布与模板（`conversations.ts`、`canvases.ts`、`canvas.ts`、`templates.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/conversations` | `filter`, `q`, 分页 | `ConversationList` | mock |
| GET | `/conversations/{id}` | | `Conversation` | mock |
| POST | `/conversations` | `{ text, context?, model? }` | `{ id }` | implemented |
| POST | `/conversations/{id}/messages` | `{ text, model?, reasoning?, attachments? }` | `{ message, reply_id }`，随后由事件流推送回复 | mock |
| GET | `/conversations/{id}/events` | | SSE：`StreamEvent`（见「流式对话」）；回复结束后服务端保存完整消息 | mock（前端模拟） |
| GET | `/canvases` | `kind`, `q`, 分页 | `{ items: CanvasSummary[], counts }`；`kind`：investigation / plan / review / analysis / custom | mock |
| POST | `/canvases` | `{ title, kind, template_id, goal, services, ai_draft }` | `{ id, href }`；调查类返回 `INV-*` | mock |
| GET | `/canvases/{id}` | | `CanvasDoc`（非调查类画布：通用节点块、边、产物、各节点对话） | mock |
| POST | `/canvases/{id}/nodes/{node}/messages` | `{ text, model?, reasoning? }` | `{ reply }` | mock |
| GET | `/canvas-templates` | | `{ items: CanvasTemplateOption[] }` | mock |
| POST | `/canvas-templates` | `{ from_canvas, name, keep }`（只保存结构与只读步骤） | 模板 | mock |
| GET | `/investigations/{id}/canvas` | | `CanvasGraph` | mock |
| POST | `/investigations/{id}/branches` | `{ parent, title }` | `CanvasNode` | mock |
| PATCH | `/investigations/{id}/nodes/{node}` | 位置、折叠、标题 | `CanvasNode` | mock |
| POST | `/investigations/{id}/nodes/{node}/messages` | `{ text, model?, reasoning? }` | `{ summary, new_branch? }`；`new_branch` 表示 AI 在该节点下派生了子分支（标题、副标题、摘要） | mock |
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
| GET | `/services/{id}/peek` | | `ServicePeek`（悬浮卡） | mock |
| GET | `/people/{name}/peek` | | `PersonPeek`（悬浮卡） | mock |
| GET | `/incidents` | `status: active\|resolved\|all` | `{ items, counts }` | implemented |
| GET | `/incidents/{id}` | | `Incident` | mock |
| POST | `/incidents/{id}/timeline` | `{ text }` | 时间线条目 | implemented |
| GET | `/alert-rules` | | `AlertRuleSummary[]` 与完整可见范围的 `all/firing/scheduled` 计数；仅展示服务端按 Prometheus 规则文件和组明确绑定到应用、且标签和环境均匹配的规则；来源失败或超限明确报错，不返回部分计数；无可信周期来源时 `scheduled=0` | implemented |
| GET | `/alert-rules/{id}` | | `AlertRule` | mock |
| POST | `/alert-rules/{id}/dry-run` | `{ expr, for }` | `{ would_fire, windows }` | mock |
| PUT | `/alert-rules/{id}` | `AlertRule` | `AlertRule` | mock |
| GET | `/notification-routes` | | `{ root: RouteNode }` | mock |
| GET | `/silences` | `state: active\|planned\|expired\|all` | 静默列表及当前可见记录计数；应用标签需在服务端显式绑定到当前环境的有效应用 ID，再按应用/团队授权判断；环境明确但应用范围较宽的静默仅环境级静默管理员可见；无精确环境或匹配器歧义的静默不展示，宽范围只报告覆盖标志 | implemented |
| GET | `/inhibitions` | | `{ items: Inhibition[] }` | mock |
| POST | `/silences/preview` | 匹配器 | `{ affects, sample }` | mock |
| POST | `/silences` | 匹配器 + 时长 | `{ id }` | mock |
| DELETE | `/silences/{id}` | | 204 | mock |
| GET | `/proposals/{id}` | | `Proposal` | implemented |
| POST | `/proposals/{id}/approve` | `{ comment? }`（需通行密钥二次验证） | `{ execution_id }` | mock |
| POST | `/proposals/{id}/reject` | `{ comment? }`；仅待审提案，由人类身份执行 | 更新后的提案 | implemented |
| POST | `/proposals/{id}/comments` | `{ text }`；可选 `Idempotency-Key` 请求头 | 评论 | implemented |
| GET | `/executions/{id}` | | `Execution` | mock |
| POST | `/executions/{id}/report/publish` | | 发布结果 | mock |

## 知识与记忆（`knowledge.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/runbooks` | `q`, `service` | Runbook 列表 | mock |
| GET | `/runbooks/{id}` | | `Runbook` | mock |
| POST | `/runbooks/{id}/dry-run` | 验证当前持久化步骤，不执行外部动作 | 演练结果 | implemented |
| POST | `/runbooks/{id}/submit` | 当前持久化草稿，无请求体 | 提交审核 | implemented |
| GET | `/postmortems` | | `PostmortemSummary` | mock |
| GET | `/memory` | | `MemoryView` | mock |
| POST | `/memory/facts/{id}/{action}` | `action: confirm\|reject\|expire` | 更新后的事实 | mock |
| POST | `/memory/conflicts/resolve` | `{ keep, drop }` | 结果 | mock |
| GET | `/knowledge-sources` | | 知识来源 | mock |
| POST | `/knowledge/search` | `{ q, service?, top_k? }` | `{ hits: RetrievalHit[] }` | implemented |

## 资源与观测（`observe.ts`）

只读聚合；遥测数据按需从连接器查询，不落平台。

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/services` | `q`, `tier`, `team`, `status` | `ServiceCatalog` | mock |
| GET | `/services/{id}` | | `ServiceDetail` | mock |
| GET | `/services/{id}/resilience` | | `Resilience`（灰度 / 熔断 / 限流 / 开关 / HPA，只读） | mock |
| POST | `/services/{id}/resilience/proposals` | `{ kind, params }` | `{ proposal_id }`（只生成提案或合并请求） | mock |
| GET | `/topology` | `cluster`, `around`, `depth`, `group_by` | `Topology`；当前后端仅返回经授权的 CMDB 节点、关系和缺失字段清单，完整 DTO 尚未实现 | mock |
| GET | `/metrics/dashboard` | `service`, `range`, `align` | `MetricsDashboard` | mock |
| POST | `/metrics/ask` | `{ view, question }` | `{ conversation_id }` | mock |
| GET | `/traces` | `scope: app\|agent`, `view`, `q`（TraceQL / 过滤表达式）, `service` | `TraceList`：分面计数、保存的视图、按状态直方图、耗时散点、表格 | mock |
| GET | `/traces/{id}` | | `TraceView`：Span（关键路径、属性、事件、日志、关联）、火焰图说明、服务调用图 | mock |
| GET | `/services/discover` | `cluster` | 只读发现未登记的工作负载与负责人、系统映射线索 | mock |
| POST | `/services` | `{ items: [{ id, namespace, tier, owner, slo, p99 }] }` | `{ created }` | implemented |
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
| POST | `/notification-routes/test` | 路由草稿 + 样例告警 | 命中路径 | mock |
| POST | `/notification-routes` | 匹配条件、接收方、分组、时段、升级 | `{ id }` | mock |
| POST | `/inhibitions/preview` | `{ source, target, equal }` | 过去 7 天会抑制的数量与样例 | mock |
| POST | `/inhibitions` | `{ source, target, equal }`（P1 永不抑制） | `{ id }` | mock |
| GET | `/runbooks/draft` | `source: template\|postmortem\|conversation\|import`, `from` | Runbook 草稿（写操作步骤标记 `approval`） | mock |
| POST | `/runbooks` | `{ title, source, from?, steps, status: draft\|review }`；非空白来源需验证已有引用，空白来源不得填写引用 | `{ id }`；复核通过前 Agent 不使用 | mock |
| GET | `/postmortems/candidates` | | 有真实结案证据、尚无复盘且当前有权访问的事件；含实际证据数与持续时长 | implemented |
| GET | `/postmortems/draft` | `incident` | 复盘草稿 | mock |
| POST | `/postmortems` | `{ incident_id, sections, actions, status: draft\|review }` | `{ id }`；同事务锁定已解决事件并写入复盘与审计，不触发外部动作 | implemented |
| POST | `/knowledge-sources/test` | `{ type, url, secret_ref: "vault:…" }` | 连接结果 | mock |
| POST | `/knowledge-sources` | `{ type, url, secret_ref: "vault:…", spaces, schedule, chunk, inherit_acl }`（只接受 Vault 引用） | `{ id, status }` | mock |

## Agent 链路（`traces.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/agent-runs/{id}/trace` | | `AgentTrace`：汇总、会话各轮模型、Span 树（agent / chain / llm / tool / retriever / guardrail），LLM 消息、参数、token 构成、评分，工具参数与结果 | mock |

## 集成中心（`integrations.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/connectors` | `category`, `q`（名称、标识和供应商支持中文及拼音检索） | `ConnectorList`；计数基于当前可见全集，筛选只影响 `items`；`ok/error` 是最近 15 分钟的连接探测结果，不代表持续健康，过期显示 `unverified` 并保留上次检查时间；未分页的可见记录超过 1000 条返回 413 | implemented |
| GET | `/connectors/{id}` | | `ConnectorDetail` | mock |
| POST | `/connectors/{id}/test` | | `{ ok, latency_ms }` | implemented |
| PATCH | `/connectors/{id}` | 如 `{ tools: [{ name, enabled }] }` | 更新结果 | mock |
| POST | `/connectors` | `{ type, url, auth, tools, webhook, credential_ref? }`；凭据仅传 Vault 引用，缺凭据/项目范围的 GitLab 创建请求保存为未启用登记，`webhook` 仅记录意图，不自动创建 Webhook | `{ id }`；未启用登记不可测试连接或启用工具，补齐凭据及项目绑定后可通过辅助接口激活 | implemented |
| GET | `/connector-types/{type}` | | `ConnectorType`（认证方式、权限范围、只读工具） | mock |
| GET | `/mcp-servers` | | `McpList` | mock |
| GET | `/mcp-servers/{id}` | | `McpDetail` | mock |
| PATCH | `/mcp-servers/{id}/tools/{name}` | `{ enabled }`（写入工具开启后仍只能以提案使用） | 结果 | mock |
| POST | `/mcp-servers/{id}/review` | 审核新版本发现的工具 | 结果 | mock |
| GET | `/ingest/pull/{id}` | | `PullDraft`（请求模板、样例、字段映射、预览） | mock |
| POST | `/ingest/pull/{id}/test` | 映射 | `{ ok, rows, dropped }` | mock |
| GET | `/ingest/push` | | `PushView`（事件类型、接口说明、投递记录、配套栈） | mock |
| POST | `/ingest/push/test` | `{ type }` | `{ code }` | mock |
| GET | `/service-mappings` | | `ServiceMapping` | implemented |
| POST | `/service-mappings/accept` | `{ cells: ["service:system"] }` | `{ accepted }` | mock |
| POST | `/service-mappings/discover` | | `{ suggestions }` | mock |
| PUT | `/service-mappings` | 完整映射 | 结果 | mock |
| GET | `/coverage` | `app` | `CoverageView`（覆盖矩阵、对 Agent 的影响、修复建议） | mock |
| GET | `/webhooks` | | `WebhookView`（入站、令牌、对外 MCP、出站） | mock |
| POST | `/webhooks/{id}/redeliver` | | 结果 | mock |
| POST | `/api-tokens` | `{ name, scopes, ttl }` | `{ id, secret_shown_once }` | mock |

Push 事件接入（供外部系统调用，不是前端调用）：`POST /api/v1/events/{alert|change|incident|resource|oncall}`，请求头 `Authorization: Bearer`、`Idempotency-Key`、`X-Signature` + `X-Timestamp`；返回 `202 / 409 / 422 / 401`。

连接器辅助激活接口（不计入 166 项页面契约）：`POST /connectors/{id}/activate`，在单一环境下传 `{ credential_ref: "vault://mount/path#field" }`。当前仅支持 GitLab：须先通过服务环境的显式目标绑定登记至少一个数字项目 ID；后端用 Vault 凭据对每个项目发起 GET 验证，再复核权限、连接器配置和绑定集合，成功后启用连接器。验证并不证明令牌本身只有只读权限。工具仍保持关闭，`webhook` 仅记录意图；上游项目验证失败返回 502 且不启用，项目绑定变化须重新激活。返回 `{ enabled: true, status: "ok", verified_projects, webhook_active: false }`，表示一次验证结果，不代表持续健康。

## Agent 运行台（`agents.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/agents/lifecycle` | | `Lifecycle` | mock |
| GET | `/agents/{id}` | | `AgentDetail` | mock |
| POST | `/agents/{id}/rollout` | `{ to, percent, override }`（`override` 表示管理员放行未通过的门禁，写审计） | 结果 | mock |
| POST | `/agents/{id}/rollback` | | 结果 | mock |
| PATCH | `/agents/{id}/level` | `{ level: L0..L3 }`（受环境上限约束） | 结果 | mock |
| GET | `/prompts` | | `{ items: PromptItem[] }` | mock |
| GET | `/prompts/{id}` | | `PromptDetail` | mock |
| POST | `/prompts/{id}/trial` | `{ case, a, b }` | `PromptTrial[]` | mock |
| POST | `/prompts/{id}/versions` | `{ text }` | `{ version }` | mock |
| GET | `/agent-tools` | `kind: all\|read\|write` | `ToolsView` | mock |
| GET | `/evals/{id}` | | `EvalExperiment` | mock |
| POST | `/evals` | 实验配置 | `{ id }` | mock |
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
| GET | `/me/profile` | | `ProfileView` | mock |
| PUT | `/me/profile` | `ProfileView` | `ProfileView` | mock |
| DELETE | `/me/sessions/{id}` | | 204 | implemented |
| GET | `/members` | `filter: all\|admin\|no_mfa\|disabled`, `q` | `MembersView` | mock |
| POST | `/members/invitations` | `{ email, role? }` | 结果 | mock |
| POST | `/members/sync` | | `{ added, updated }` | mock |
| POST | `/members/mfa-reminders` | | `{ sent }` | mock |
| GET | `/roles` | | `{ items: Role[] }` | implemented |
| GET | `/roles/{id}` | | `RoleDetail` | mock |
| PUT | `/roles/{id}` | `{ matrix }`（机器身份的"批准"列必须拒绝） | 结果 | mock |
| GET | `/security` | | `SecurityView` | mock |
| PUT | `/security` | `SecurityView` | `SecurityView` | mock |
| GET | `/me/notification-settings` | | `{ quiet: { enabled, from, to }, digest: off\|daily\|weekly, subscriptions }`；只读取当前账号已保存偏好，无记录时返回平台默认值，不代表通知渠道已接通 | implemented |
| PUT | `/me/notification-settings` | 同上 | 同上 | mock |
| GET | `/me/ai-preferences` | | `{ default: ModelChoice, canvas_inherit, show_reasoning, auto_context, memory, budget_warn_tokens }`；`GET /models` 的 `default` 取自这里 | implemented |
| PUT | `/me/ai-preferences` | 同上 | 同上 | implemented |
| GET | `/workspace` | | `{ name, slug, timezone, locale, week_start, retention_days, data_region, brand }` | mock |
| PUT | `/workspace` | 同上（仅管理员可改，写入审计） | 同上 | mock |
| GET | `/audit` | `filter: all\|user\|agent\|system`, `q` | `AuditView` | implemented |
| GET | `/audit/{id}` | | `AuditDetail`（含哈希链校验结果） | implemented |
| POST | `/audit/export` | 过滤条件 | `{ url }` | implemented |

## 后端辅助接口（不计入上述 166 项）

- `GET /changes/records?service_id=…&binding_id=…&page=1`：只读已显式绑定的 GitLab 项目流水线，要求当前环境的 `services.view` 与 `connectors.view`，每页 20 条，最多查询第 1000 页；前后复核授权与来源。只返回可核实的流水线身份、状态、分支、提交及上游时间，不包含变更风险、告警关联或看板 KPI，不替代主 `GET /changes`。
- `GET /services/{id}/external-targets`：按服务 ID 内的应用与环境读取显式登记的外部目标，需 `services.view` 与 `connectors.view`；每个服务环境最多登记 100 条，不返回端点或凭据。旧绑定不自动回填。
- `POST /services/{id}/external-targets`：传 `connector_id`、`target_kind`、`external_id`，当前仅接受同环境 GitLab 连接器的正整数项目 ID；需 `services.change` 与 `connectors.manage`，同一外部目标不可归属于两个服务，成功写入审计。该登记本身不授予外部写入权限。
- `DELETE /services/{id}/external-targets/{bindingId}`：在相同范围与权限下删除指定登记并审计；不删除外部项目。
- `GET /postmortems/records?tab=all|draft|review|published`：按当前环境及事件、知识对象权限读取已保存复盘的事件、级别、结案时间、持续时长、状态与行动项数量；最多返回 1000 条可见记录。此接口不替代 `PostmortemSummary`，不推测根因分类、行动完成率或看板 KPI。
- `PUT /mcp-servers/{id}/environment-binding`：用请求头选择单一环境，把现有声明为只读且已经人工审核的 MCP 登记显式绑定到该环境；要求对所属应用有 `connectors.manage`，幂等并记录变更审计。该绑定仅使登记元数据可见，不代表端点、固定参数或凭据已经通过所选环境的执行安全审查。
- `DELETE /mcp-servers/{id}/environment-binding`：从请求头指定的单一环境解绑该登记；同样检查所属应用权限，幂等并记录实际变更。旧登记不会自动绑定或复制到其他环境。

## 变更记录

| 日期 | 变更 |
| --- | --- |
| 2026-09-28 | 冻结 v1。拆分合并行；补 `GET /conversations/{id}/events` 行；画布节点对话请求加 `model`、`reasoning`，响应加 `new_branch`；新增实例与环境一节和 `X-AIOps-Environment`。 |

## 后端实现提示

- 后端实现一个端点后，把状态改成 `implemented`，并删除或保留对应的 Mock（保留时仅用于开发）。
- 字段有分歧时先改 `types.ts`，再让 Mock 和后端同时跟进；前端 `npx tsc -b` 会暴露所有受影响的页面。
- 推理强度（v4）：后端目前支持 OpenAI 兼容 Chat Completions（含 Ollama）与 Claude Messages，尚无 Responses API 与强度透传。`GET /models` 的档位由后端按模型声明返回；映射为 `reasoning.effort`（Responses）、`reasoning_effort`（Chat Completions）、Claude 的 thinking 参数或 Ollama 的 `think` 开关。
- 示意数据只在 Mock 模式下出现，页面顶部有"示意数据"标记；真实模式下缺失的能力应返回空列表或明确的"待接入"状态，不能用示意数据代替。
