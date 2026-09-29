# v3 前端 API 契约（Web ⇄ 后端）

- 状态：**契约冻结 v1（2026-09-28）**。前端已按本契约用 Mock 实现；后端按接口逐项迁移，`mock` 仍表示尚未达到后端完成标准，即使已有部分处理逻辑。历史：2026-09-24 初版；v4 增补模型与推理强度、附件、通用画布、各"新建"流程、链路 v2；2026-09-28 增补实例与环境。
- 表格每行只写一个方法和一个路径。`scripts/check-v3-contract.py` 会核对本文件和 Mock 路由是否一致，改契约后运行它。
- 冻结后的修改流程：先改本文件和 `types.ts`，再改 Mock 与后端；破坏性修改（删字段、改语义）在「变更记录」里写明。
- 类型真相源：`web/src/v3/api/types.ts`。本文件只描述路径、参数和语义，字段以该文件为准。
- Mock 实现：`web/src/v3/mock/domains/*.ts`，每个文件对应下面一节；`VITE_API_MODE=http` 时前端直接请求真实后端。
- 与既有 `docs/api-contract.md` / `internal/apicontract/openapi.json` 的关系：v3 为新 UI 所需的读模型。后端落地时优先复用既有资源（调查、分支、提案、执行、Runbook、连接器）；能直接对齐的路径就对齐，对不齐的在 OpenAPI 中新增，再同步 `scripts/build-openapi.py`。本文件的"状态"列在后端实现后改为 `implemented`。
- `scripts/check-v3-contract.py` 核对的 165 条 Mock 路由，加上前端模拟而无 Mock 路由的 SSE `GET /conversations/{id}/events`，共 166 条。`implemented` 只表示后端真实处理、范围鉴权、契约和定向检查具备；不表示外部系统已实连或前端 HTTP 模式已验收。阶段进展与未完成项见 [后端实施记录](../plans/2026-09-28-v3-backend-implementation.md)。
- 并行后端会话共用的 DTO、实例/环境、授权、错误、审计及迁移编号约束见 [V3 后端并行集成基线](v3-integration-baseline.md)；该文件不改变本契约的字段、路径或完成状态。

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

服务端断线续传头 `Last-Event-ID` 只接受单个非负十进制事件 ID；空值、重复头、负数、非数字或溢出返回 `422 invalid_event_cursor`。正数游标必须属于当前有权会话，续传该事件所在的回复，即使之后已有新回合；已删除或未知游标返回 422。缺省或 0 游标读取当前最新回复。早期失败也先补空 `message.started`，再发状态与 `message.completed`。这些后端保障不改变该主路由的 `mock` 状态。

当前后端在创建回合及成功或失败的结束事务中重核持久会话、所有者、环境和 V3 对话权限；租约恢复也重核，撤权后的未完成输出不会保存为成功回复。SSE 在连接和每批事件读取前重新核对当前查看权限；活动回复还核对原发起会话及 `conversation.change`，已完成回复按查看权限回读。授权检查之后到事件查询或网络发送之间仍有并发撤权窗口，已经发出的字节无法撤回；真实权限策略下的已打开流逐批撤权尚未实测。这些安全补口不构成完整页面验收。

| 事件 | 含义 |
| --- | --- |
| `message.started` | 助手消息开始，携带空 `parts` |
| `part.added` | 新增一个富消息部件（`tool_calls`、`text`、`chart`、`table`、`citations`、`proposal`…，见 `Part`） |
| `text.delta` | 向第 `index` 个 `text` 部件追加 `delta` |
| `message.completed` | 结束，前端关闭 EventSource |

**按轮选择模型（v4）**：请求体可带 `model`（`provider:model`，来自 `GET /models`）与 `reasoning`（该模型声明的档位 id）；缺省时用 `GET /models` 的 `default`。一轮只用一个模型，回复进行中切换只影响下一轮。`message.completed.run` 仅在取得可信模型用量时出现；存在时才记录 `model_id`、`model`、`reasoning`、`reasoning_label`、`brand`，不能为缺失用量补零。冻结前端约定附件先经 `POST /uploads` 上传，再以 `attachments: Upload[]` 随消息提交，附件作为数据处理。当前后端模型请求只有文本：即使附件引用有效，也在创建用户回合前返回 `422 attachment_model_unsupported`；无效引用同样不会创建回合。前端的附件发送与 422/SSE 错误恢复尚未验收，所以主 POST/SSE 均保持 `mock`。

调查画布节点对话仍规划复用富消息部件与事件；当前通用画布节点 `POST /canvases/{id}/nodes/{node}/messages` 是持久化同步 `{ reply }`，不使用上述 SSE 流。

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

发现路由当前可按真实持久来源检索有权服务、事件、调查、有效人员与本人对话，支持中文与拼音，并分别将查询结果限制为 8 条搜索命中或 30 条提及候选；非空查询中任一来源类型匹配过宽时返回 413，不返回不完整成功结果。“最近访问”无真实行为记录，告警组、产物和 Runbook 预览无可用独立来源。调查结果暂链接画布列表，前端提及页签也尚未按类型重新查询；这三条主路由仍为 `mock`。

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

配置提升后端当前只支持 `template` / `templates` 请求值对应的**已净化画布模板**，不支持本页其余三类；不支持的类型返回 `501 promotion_kind_unavailable`。非生产导出要求选定单一环境、模板本人所有且有 `canvas.change` 授权，生成签名清单并向独立 GitLab 配置仓库提交 MR；生产导入只接受 `mr-<正整数 IID>`，要求人类平台管理员及全局 `workspace.manage`，在事务中再次核权，并验证已合并 MR 的固定提交、单一预期文件、来源签名及净化结构。导入还查询当前 MR 审批已满足、最终差异形成后且合并前的独立非 bot 审批，以及当前目标分支保护配置；这些 API 不能证明**合并当时**的治理策略，真实仓库上线前仍须保留审核与分支策略的部署证据，不能仅凭 `merged` 声称已审核。生产 `GET /config-sync` 列出的至多 100 条是**待验证候选元数据**，完整来源验证只在显式导入时发生；非生产列表仅含当前操作者在有权环境导出的至多 100 条。超限返回 413；未配置仓库、签名密钥或可信公钥返回 501；不可验证来源返回 503；`all` 写操作返回 409。本机私有 GitLab CE 项目已验证适配器协议与拒绝边界，尚未完成实际部署仓库的服务端导入或页面联调；前端仍展示四类配置，因此三条主契约保持 `mock`。

导入同一变更重复请求收敛为一条台账和成功审计；目标模板或导入台账发生唯一冲突时返回 409，事务不留下半成品。独立来源/生产 schema 的 HTTP 往返与新 PG 连接读回只验证本地持久化；仓库替身给出已合并状态，不能等同于真实 GitLab 的完整审批链。每来源环境 ID 的独立允许列表尚未配置。

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
| GET | `/canvases/{id}` | | `CanvasDoc`（非调查类画布：通用节点块、边、产物、各节点对话） | implemented |
| POST | `/canvases/{id}/nodes/{node}/messages` | `{ text, model?, reasoning? }` | `{ reply }` | implemented |
| GET | `/canvas-templates` | | `{ items: CanvasTemplateOption[] }` | mock |
| POST | `/canvas-templates` | `{ from_canvas, name, keep }`；当前后端子路径只保存本人同环境通用画布的私有结构模板，`keep.steps/runbooks/content=true` 返回 422 | 模板 | mock |
| GET | `/investigations/{id}/canvas` | | `CanvasGraph` | mock |
| POST | `/investigations/{id}/branches` | `{ parent, title }` | `CanvasNode` | mock |
| PATCH | `/investigations/{id}/nodes/{node}` | 位置、折叠、标题 | `CanvasNode` | mock |
| POST | `/investigations/{id}/nodes/{node}/messages` | `{ text, model?, reasoning? }` | `{ summary, new_branch? }`；`new_branch` 表示 AI 在该节点下派生了子分支（标题、副标题、摘要） | mock |
| GET | `/investigations/{id}/writeback-preview` | `node` | 回写预览 | mock |
| POST | `/investigations/{id}/writeback` | `{ node, target, items }` | `{ version }` | mock |

对话详情、后续模型上下文和最新回复使用持久 `sort_order`，同一事务写入的用户/助手消息按回合排序。有 `accepted` 审计的回合按序列化顺序；迁入或无审计的旧回合只按时间戳作确定性回填，不能据此证明原始提交顺序。迁移 085 在应用启动事务中回填整表并建索引，部署前须停旧回复 worker 并在代表性副本量测锁时。

`GET /conversations` 的 `mine` 与 `archived` 现在在同一授权快照中合并本人持久对话和私人通用画布，按当前会话、角色来源、环境及各模块查看权限复核，搜索、分页、总数与已知分面保持一致。`team`/`triage` 缺可靠统一来源时返回 503；相应 `counts` 为 `null`，`stats` 为空。历史调查缺可靠的本人/参与者归属，画布回合缺用量时 `tokens` 为 `null`，不以部分数字冒充总用量。当前前端将 `tokens` 当数字且列表入口只核 `conversation.view`，画布专属用户及未知用量的页面体验仍未接通；主读取路由保持 `mock`。

通用画布模板保存已在同一事务复核会话、本人同环境源画布和当前 `canvas.change`，只复制净化后的节点/边布局及用户提交的模板名称，排除源节点标题、内容块、产物与对话；图结构验证失败不保存模板或审计。模板名称仍是可持久化文本：当前前端名称留空时会沿用源画布标题，可能包含需用户检查的业务信息。前端默认提交 `steps=true`、`runbooks=true`，而后端明确 422；页面还称“团队模板”，但当前仅为私有模板，且列表不含调查模板。故 `GET/POST /canvas-templates` 仍为 `mock`。

通用画布节点 `POST /canvases/{id}/nodes/{node}/messages` 的后端已接持久化同步回复：限定本人画布、单一环境和当前 `canvas.change`，一次只运行一条回复。模型上下文包含当前节点及沿有向边可达的祖先节点块与已完成回合，不包含兄弟或失败回合，超过 24 KiB 返回 413；工具调用被拒绝。运行期间约每 2 秒检查当前会话与授权，失败时取消模型请求；完成事务再次核对原会话、画布归属与当前 `canvas.change`，完成时无权限则不保存助手回复。若撤权又恢复全部落在两次检查之间，不能保证取消本轮。失败的用户输入保留供刷新核对，不生成虚假助手消息。`GET /canvases/{id}` 校验节点、七种块、边和产物结构，在 `threads` 返回各节点完整已保存回合；历史超过 500 次交换、2 MiB 总量或单条助手消息 64 KiB 时返回 413，不截断。写入仍可继续，故达到上限后详情会持续 413，当前没有分页或归档恢复。两条主路由按后端判据标为 `implemented`，仅适用于私人通用画布；调查节点接口仍分开。前端尚无超限恢复/分页，发送失败也未清除 `busy`，真实 409/413/422/503 可卡住输入框，因此画布整页尚未验收。
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

`POST /silences/preview` 的后端现按所选单一环境读取实时 Alertmanager 告警快照。请求为 `{ matchers: [{ k, op, v }] }`，支持 `=`、`!=`、`=~`、`!~`（1–20 条）；环境是隐含的精确匹配。精确 `application=` 值必须在服务端绑定到有效应用并有当前应用/团队 `silences.view`，无应用匹配则要求环境级 `silences.view` 与 `silences.manage`。`affects` 是此刻该环境匹配告警的完整条数，`sample` 仅为最多 5 个去重排序的真实 `alertname`；来源异常、超过 500 条该环境告警或异常 `null` 均返回 503，不给部分计数。预览不会创建静默。现有页面默认仅传 service/alertname，普通应用用户会因缺精确应用匹配收到 403，且页面尚未正确清除旧预览/失败状态，故该主契约继续 `mock`。`POST /alerts/{id}/claim` 已改为持有事件行锁时复核当前授权，仍只返回 `{ id, assignee_id }`，不等于完整告警详情。

底层 Alertmanager 适配器可按 v2 协议创建和过期静默：创建不带客户端 ID，校验返回 UUID；过期使用单数 `/silence/{id}` 路径；超时等结果不确定的写入单独标记。创建台账现能持久保留当前有权人类操作者的环境/应用作用域、固定载荷、幂等键和审计；它没有生产调用者。私有对账会枚举一个配置端点的完整静默列表：零条标记保持未知且不重发，唯一完整匹配才确认，重复或失配进入人工复核；过期时间窗会在保留后执行与低层发送前拒发。单端点快照不能证明 HA 集群最终收敛后的全局唯一。生产执行前仍须在同一事务核验具体持久提案及人工批准，并由独立执行器按明确的未发送/不确定结果路径调度。删除还缺对象归属、授权和持久意图；反向代理的 404 不能证明对象已删除。未注册 `POST /silences` 或 `DELETE /silences/{id}` 产品路由，两条主契约继续 `mock`。

`GET /inhibitions` 现在仅展示来源/目标都绑定当前环境、应用归属有效且当前用户有相应应用或团队 `silences.view` 的规则；宽应用范围要求同环境的查看与管理授权。跨环境、匹配器歧义与失效绑定不会出现在列表；任一已授权上游来源失败时返回 503，不给部分规则。成功响应始终为 `206 partial`，因为 Alertmanager 配置没有可信的逐规则活动状态或命中数；故完整 `Inhibition[]` 主契约仍为 `mock`。

## 知识与记忆（`knowledge.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/runbooks` | `q`, `service` | Runbook 列表 | mock |
| GET | `/runbooks/{id}` | | `Runbook` | mock |
| POST | `/runbooks/{id}/dry-run` | 当前对象需 `knowledge.manage`；验证持久化步骤并保存预览/审计，不执行外部动作 | 演练结果 | implemented |
| POST | `/runbooks/{id}/submit` | 当前持久化草稿，无请求体 | 提交审核 | implemented |
| GET | `/postmortems` | | `PostmortemSummary` | mock |
| GET | `/memory` | | `MemoryView` | mock |
| POST | `/memory/facts/{id}/{action}` | `action: confirm\|reject\|expire` | 更新后的事实 | mock |
| POST | `/memory/conflicts/resolve` | `{ keep, drop }` | 结果 | mock |
| GET | `/knowledge-sources` | | 知识来源 | mock |
| POST | `/knowledge/search` | `{ q, service?, top_k? }` | `{ hits: RetrievalHit[] }`；关键词与向量共用当前显式环境、有效应用绑定和 V3 授权过滤，团队标记另需匹配当前负责人及团队查看权限；输出前按文档 ID/版本重核。历史归属不明、异常元数据和未验证登记来源不进入结果 | implemented |

知识治理另有部分证据与人工审核辅助接口，详见 [V3 后端辅助接口](v3-backend-aux.md)。`GET /runbooks/{id}` 在同一只读快照中核对文档范围、当前对象授权与结构化步骤：隐藏或非 Runbook 为 404；有权旧文档缺步骤为 `503 structured_steps_unavailable`；有步骤但缺完整用量、事件处置、审核来源与执行核验时为 `503 runbook_read_unavailable`，不补零冒充完整 DTO。已落库字段可从 `/runbooks/{id}/record` 读取，主详情继续 `mock`。`GET /memory` 当前明确返回 `503 memory_view_unavailable`，只提供 `/memory/facts` 的受权、当前来源事实列表；没有可信的层级、置信度、召回次数与过期值。事实状态操作仅支持 `confirm`、`reject`、`expire`，冲突裁决须传入两个真实事实 ID。现有前端的 `forget` 和冲突请求体不符合此接口，因此上述主页面行保持 `mock`。

V3 知识正文读取按持久环境、有效应用与当前授权失败关闭；带 `team_id` 的文档还要求该值与应用当前负责人一致并有团队查看权限。标记了 `source_registration_id` 的来源文档在来源证明链闭合前不参加 V3 检索或来源投影。可选的 `search_knowledge` Agent 证据组件尚未在正式进程注册；V3 实例关闭旧 Worker，不能把旧检索工具当作当前 V3 调用路径。真实来源正文同步、索引归属与完整页面 DTO 仍待接入。

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
| GET | `/services/discover` | 单个可选 `cluster`；重复参数返回 422 | 只读发现经授权的现存工作负载、稳定资源 ID 和未经核实的负责人提示；同名歧义候选不返回 | mock |
| POST | `/services` | `{ items: [{ id, namespace, resource_id?, tier, owner, slo, p99 }] }`；`resource_id` 为发现接口返回的稳定 CMDB 标识，提供时必须有 namespace | `{ created }` | implemented |
| GET | `/logs` | `q`, `levels` | `LogSearch`（含 AI 聚类的日志模式） | mock |
| GET | `/changes` | `range`, `sources` | `ChangesView`（CI/CD 与 GitOps 事件，只读） | mock |

资源发现现在返回 `resource_id`、Kubernetes `uid`、环境与集群；单集群条件在 2000 个候选扫描上限之前执行，整次请求共用 20 秒预算。选定集群之外的可信同名资源，以及同环境、同命名空间中陈旧、缺失或已关联的原始同名身份，均会阻止当前只按名称登记的候选进入结果；`ambiguous_workloads` 只给出数量，不泄露阻挡对象。`registered` 是授权环境内的登记数，不是所示集群的登记数。负责人和等级只作来源提示，`replicas` 可为空，`matched`/`missing` 尚无可信线索。注册时按当前环境、采集新鲜度、存在状态、UID、命名空间、是否已关联及 `resources.view` 重新核对。旧客户端只传名称时仅可匹配唯一工作负载；同名、过期或已关联身份返回 409，不会静默关联。当前前端未传 `cluster`/`resource_id`，并固定展示负责人、SLO、P99 与系统映射，发现到注册交互尚未接通。服务列表和系统映射超过 1000 条可见记录会返回 `422 result_limit`，不会返回不完整总数。拓扑仅显示五分钟内成功采集且环境吻合的现存节点，关联应用不会替资源借用其团队授权。

观测查询只使用持久化应用、环境、负责人团队及已配置的外部目标绑定；不同来源绑定冲突返回 `409 ambiguous_binding`，范围过大返回 `422 scope_limit`。普通日志查询返回 `503 log_patterns_unavailable`，其中 `available` 仅包含真实总数、错误数、直方图和有界样本，`missing` 标明模式计数与趋势；只有显式空 `levels` 返回完整空结果。Trace 列表返回 `503 trace_aggregation_unavailable`，只提供 `observed_sample` 与缺失的总数/分面/直方图字段；详情 `200` 标记 `partial: true` 和 `missing`，不能把受权 Span 样本当成完整链路。指标、日志、Trace 主页面均保持 `mock`。

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
| POST | `/knowledge-sources/test` | `{ type, url, secret_ref? }`；公开 HTTPS 单页 Markdown 可无凭据，其他类型需 Vault 引用 | 连接结果 | mock |
| POST | `/knowledge-sources` | `{ type, url, secret_ref?, spaces, schedule, chunk, inherit_acl }`；公开 HTTPS 单页 Markdown 可无凭据，其他类型需 Vault 引用 | `{ id, status }` | mock |

`GET /runbooks/draft` 目前只有传入真实模板 ID 的 `template` 来源可返回无步骤大纲；对话、复盘和导入来源通过验证后返回 `501 draft_generator_unavailable`。前端尚未为模板、对话和导入入口传 `from`，草稿页仍为 `mock`。`GET /postmortems/draft` 仅拼接有结案证据的事件信息与时间线，影响、根因、行动建议均留空，不是 AI 生成稿；主契约同样保持 `mock`。公开 Web 来源仅支持持久应用绑定、无凭据单页 HTTPS Markdown 的手动观察；其他类型仍需 Vault 引用。V3 实例须显式设置 `AIOPS_V3_PUBLIC_WEB_READER_ENABLED=true` 才能记录公开页的哈希观察，未开启时同步返回 501；没有应用归属的登记返回 422。无登记的 `/knowledge-sources/test` 缺持久应用范围，即使开关开启也返回 501。登记、受限观察与归属证据不代表对所有用户公开、正文导入或索引完成；前端完整来源 DTO 未接通。

## Agent 链路（`traces.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/agent-runs/{id}/trace` | | `AgentTrace`：汇总、会话各轮模型、Span 树（agent / chain / llm / tool / retriever / guardrail），LLM 消息、参数、token 构成、评分，工具参数与结果 | mock |

Agent Trace 后端目前只能读受信生产者上报的有界 Span 元数据；模型消息、工具参数/结果、评分和完整回合仍无生产者。工具 Span 也不证明外部执行器的两阶段审计完成，故整页保持 `mock`。

## 集成中心（`integrations.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/connectors` | `category`, `q`（名称、标识和供应商支持中文及拼音检索） | `ConnectorList`；计数基于当前可见全集，筛选只影响 `items`；`ok/error` 是最近 15 分钟的连接探测结果，不代表持续健康，过期或缺凭据显示 `unverified`；登记工具偏好不生成 Agent 能力标签；未分页的可见记录超过 1000 条返回 413 | implemented |
| GET | `/connectors/{id}` | | `ConnectorDetail`：只返回当前有权的服务绑定；凭据文案区分未配置与已登记 Vault 引用，最近探测不代表持续健康；没有 Agent 执行器或查询绑定时 `tools`、`queries` 与能力标签为空，服务超过 1000 条返回 422 | implemented |
| POST | `/connectors/{id}/test` | | `{ ok, latency_ms }` | implemented |
| PATCH | `/connectors/{id}` | `{ tools: [{ name, enabled }] }`；至少一项，`enabled` 必须是显式布尔值 | 在行锁下部分合并工具偏好并同事务审计；旧目录工具冲突返回 409，不静默删除；`enabled` 仅门控部分只读来源，不授予 Agent 执行能力；前端开关尚未发起此请求 | mock |
| POST | `/connectors` | `{ type, url, auth, tools, webhook, credential_ref? }`；凭据仅传 Vault 引用，缺凭据/项目范围的 GitLab 创建请求保存为未启用登记，`webhook` 仅记录意图，不自动创建 Webhook | `{ id }`；未启用登记不可测试连接或启用工具，补齐凭据及项目绑定后可通过辅助接口激活 | implemented |
| GET | `/connector-types/{type}` | | `ConnectorType`（真实探测器支持的认证方式；暂无类型级权限范围与可执行 Agent 工具时返回空数组） | implemented |
| GET | `/mcp-servers` | | `McpList` | mock |
| GET | `/mcp-servers/{id}` | | `McpDetail` | mock |
| PATCH | `/mcp-servers/{id}/tools/{name}` | `{ enabled }`（写入工具开启后仍只能以提案使用） | 结果 | mock |
| POST | `/mcp-servers/{id}/review` | 审核新版本发现的工具 | 结果 | mock |

当前 MCP 列表只是已显式绑定环境的登记元数据快照；KPI 仅计有权可见的登记与旧只读绑定，不表示 V3 工具已审核或可执行。详情不会把旧固定参数或未信任发现快照放入可调用 `tools`，也不会把所属应用冒充 Agent 授权应用；服务版本、连接健康、调用历史、凭据验证以及工具/资源/提示词实际数量均未知。`counts` 中的 0 只是旧 DTO 占位，`unavailable_fields` 明确列出未知项，`complete_v3_dto=false`。服务端最多扫描 2000 条候选记录；若还有第 2001 条，返回 `413 candidate_scan_limit` 而不返回部分列表。超过 1000 条有权可见登记返回独立的 `413 scope_too_large`。前端当前仍把未知数量显示为确定的零，并固定显示绿色只读/Vault 标识与无需真实请求的连接成功提示，不能据此接入或验收页面；两个主读取路由继续 `mock`。

`GET /connector-types/{type}` 只描述已支持的连接器类型及探测器可接受的认证方式，要求当前环境的 `connectors.view`；未知类型返回按 `Accept-Language` 本地化的 404。版本、上游令牌权限与 Webhook 地址没有类型级可信来源，返回空值；尚无连接器支持的 Agent 工具执行器，因此 `tools: []`。本条后端路由完成不代表新建向导的静态项目映射、事件、深链及工具说明已接线，前端目录还有未支持的类型。
| GET | `/ingest/pull/{id}` | | `PullDraft`（请求模板、样例、字段映射、预览） | mock |
| POST | `/ingest/pull/{id}/test` | 映射 | `{ ok, rows, dropped }` | mock |
| GET | `/ingest/push` | | `PushView`（事件类型、接口说明、投递记录、配套栈） | mock |
| POST | `/ingest/push/test` | `{ type }` | `{ code }` | mock |
| GET | `/service-mappings` | | `ServiceMapping` | implemented |
| POST | `/service-mappings/accept` | `{ cells: ["service:system"] }` | `{ accepted }` | mock |
| POST | `/service-mappings/discover` | | `{ suggestions }` | mock |
| PUT | `/service-mappings` | 完整映射；当前没有可接受的稳定身份与修订请求体 | `501 v3_not_implemented`，不写入 | mock |
| GET | `/coverage` | `app` | `CoverageView`（覆盖矩阵、对 Agent 的影响、修复建议） | mock |
| GET | `/webhooks` | | `WebhookView`（入站、令牌、对外 MCP、出站） | mock |
| POST | `/webhooks/{id}/redeliver` | | 结果 | mock |
| POST | `/api-tokens` | `{ name, scopes, ttl }` | `{ id, secret_shown_once }` | mock |

服务映射候选目前仅来自管理员显式绑定的单个 GitLab 数字项目目标，不按标签、名称或部署记录推断。`POST /service-mappings/discover` 在一个环境中核对服务与连接器授权、近期探测和绑定后的激活审计，保存 15 分钟候选，不直接改服务映射；超过 500 条可见未映射来源返回 422。`POST /service-mappings/accept` 在事务中复核候选唯一性、来源身份/修订、当前授权及绑定可用性，并将映射、候选状态与审计一同提交。V3 专用校验只允许与已登记来源完全相同的单个 GitLab 项目绑定，其余绑定继续交给原平台校验器；旧平台的全局 GitLab 拒绝规则没有扩大。迁移 `090_v3_service_mapping_candidates.sql` 保存候选；应用环境退役时候选会级联删除，管理审计另记接受决定。激活审计证明绑定时曾核验项目访问，近期通用探测不证明当前仍能访问项目。V3 实例拦截未迁移的旧环境编辑路由，旧模式也不能在已绑定 V3 的数据库上启动。隔离数据库 HTTP 测试覆盖候选、接受与审计，尚无正式进程对真实 GitLab 项目的端到端验收。当前前端保存调用不发送请求体，GET 映射格仅有展示值，缺少可写入的服务、环境、连接器、目标稳定 ID 和绑定修订；已登记的 PUT 在 V3 门禁中明确返回 501，空请求和展示值请求均不修改绑定或审计。前端发现后不刷新、接受前就乐观显示成功，且文案误称来自标签/命名/部署。因此三条写主路由保持 `mock`，不能把候选持久化或被拒绝的保存当作页面完成。

Push 事件接入（供外部系统调用，不是前端调用）：`POST /api/v1/events/{alert|change|incident|resource|oncall}`，请求头 `Authorization: Bearer`、`Idempotency-Key`、`X-Signature` + `X-Timestamp`；返回 `202 / 409 / 422 / 401`。

连接器辅助激活接口（不计入 166 项页面契约）：`POST /connectors/{id}/activate`，在单一环境下传 `{ credential_ref: "vault://mount/path#field" }`。当前仅支持 GitLab：须先通过服务环境的显式目标绑定登记至少一个数字项目 ID；后端用 Vault 凭据对每个项目发起 GET 验证，再复核权限、连接器配置和绑定集合，成功后启用连接器。验证并不证明令牌本身只有只读权限。工具仍保持关闭，`webhook` 仅记录意图；上游项目验证失败返回 502 且不启用，项目绑定变化须重新激活。返回 `{ enabled: true, status: "ok", verified_projects, webhook_active: false }`，表示一次验证结果，不代表持续健康。

辅助 `GET /connectors/{id}/tool-settings` 在当前 `connectors.view` 与环境范围内读取登记工具开关，返回 `connector_enabled`、`registered_tools` 和固定 `agent_execution_available: false`，不暴露端点或凭据。它只证明持久偏好；主 `ConnectorDetail.tools` 仍为空，因为没有可执行的连接器 Agent 工具。前端目前不读取该辅助接口，也不调用上述 PATCH，故主 PATCH 保持 `mock`。

## Agent 运行台（`agents.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/agents/lifecycle` | | `Lifecycle` | mock |
| GET | `/agents/{id}` | | `AgentDetail` | mock |
| POST | `/agents/{id}/rollout` | `{ to, percent, override?, override_reason? }`；放行未通过门禁须启用的人类管理员填写原因并写审计 | 仅保存发布配置，尚无真实流量路由 | mock |
| POST | `/agents/{id}/rollback` | | 结果 | mock |
| PATCH | `/agents/{id}/level` | `{ level: L0..L3 }`（受环境上限约束） | 结果 | mock |
| GET | `/prompts` | | `{ items: PromptItem[] }` | mock |
| GET | `/prompts/{id}` | | `PromptDetail` | mock |
| POST | `/prompts/{id}/trial` | `{ case, a, b }` | `PromptTrial[]` | mock |
| POST | `/prompts/{id}/versions` | `{ text }` | `{ version }`（`vN`） | mock |
| GET | `/agent-tools` | `kind: all\|read\|write` | `ToolsView` | mock |
| GET | `/evals/{id}` | | `EvalExperiment` | mock |
| POST | `/evals` | `{ agent_id, title, baseline_version, candidate_version }` | `{ id, status: "pending", execution_available: false }`，未启动评测 | mock |
| GET | `/eval-cases/{id}` | | `EvalCase` | mock |
| POST | `/eval-cases/{id}/labels` | `{ next: correct\|incorrect\|partial\|unsure, note? }` | `{ next, id }` | mock |
| GET | `/agent-runs` | `filter: all\|failed\|blocked\|negative` | `RunsView` | mock |
| GET | `/agent-runs/{id}` | | `RunDetail`（执行时间线、上下文组成、记忆） | mock |
| POST | `/agent-runs/{id}/feedback` | `{ vote: up\|down }` | 结果 | mock |
| GET | `/guardrails` | `filter: all\|blocked\|masked` | `GuardrailsView` | mock |
| PATCH | `/guardrails/{id}` | `{ enabled }`（强制规则不可关闭） | 结果 | mock |

Agent 治理接口当前只保存目录、版本、Prompt 修订、待执行评测、发布意图、反馈和护栏设置；尚无评测执行器、流量路由消费者、Prompt trial 或完整运行/护栏观测生产者。Prompt 列表只返回有真实版本的已授权记录，支持最多 160 字符的 `q`，超过 200 条可见项返回 413；详情在同一读取事务中核对 Agent、归属、授权和正文，真实正文与 `vN` 可回读，但发布状态、运行时使用关系和说明均明确为部分/未知，不冒充完整前端 DTO。`POST /prompts/{id}/versions` 对已登记且当前有权的 Prompt 原样保存非空白文本，按 `vN` 返回版本，审计与修订同事务；生产构造尚未登记 Prompt，前端默认 ID 在新库无法成功写入，因此读写主路由仍为 `mock`。`POST /prompts/{id}/trial` 明确返回 `503 prompt_trial_unavailable`，`POST /evals` 只创建 `pending` 元数据，发布和回滚不改变真实流量。部分读取返回 `null`、`partial: true` 或“未接入”；现有前端对 `score`、发布 token/门禁、评测、Trace、运行统计的 DTO 与请求体尚不兼容，不能直接切换 HTTP 模式。管理员门禁放行另需非空 `override_reason`，现有前端未提交；相关主契约行继续为 `mock`。

## 设置（`settings.ts`）

`GET /members` 当前只提供部分真实读取：在读取事务中重核账号及当前环境授权，以单条查询取成员与角色快照。响应有 `complete_v3_dto: false` 和 `unavailable_fields`；未核实的 `last_login`、`counts.no_mfa`、`mfa_grace_days` 为 `null`，`items[].mfa: "na"` 表示未知，不表示未启用。组层级、目录同步来源和 MFA 策略也无可信来源；`filter=no_mfa` 返回 `503 member_mfa_unavailable`，不会以空结果冒充筛选成功。冻结前端仍按数字显示 MFA 数量和宽限期并显示确定的安全提示，因此主接口仍为 `mock`。

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
| PUT | `/me/notification-settings` | 部分更新 `{ quiet?: boolean\|{enabled,from,to}, digest?, subscriptions?: { [existing_id]: boolean }\|现有订阅完整列表 }` | 保存并返回当前账号合并后的完整通知偏好；订阅标识与文案不能在此接口新建或修改，不代表通知已投递 | implemented |
| GET | `/me/ai-preferences` | | `{ default: ModelChoice, canvas_inherit, show_reasoning, auto_context, memory, budget_warn_tokens }`；`GET /models` 的 `default` 取自这里 | implemented |
| PUT | `/me/ai-preferences` | 同上 | 同上 | implemented |
| GET | `/workspace` | | `{ name, slug, timezone, locale, week_start, retention_days, data_region, brand }` | mock |
| PUT | `/workspace` | 同上（仅管理员可改，写入审计） | 同上 | mock |
| GET | `/audit` | `filter: all\|user\|agent\|system`, `q` | `AuditView` | implemented |
| GET | `/audit/{id}` | | `AuditDetail`（含哈希链校验结果） | implemented |
| POST | `/audit/export` | 过滤条件 | `{ url }` | implemented |

`GET /workspace` 读取持久化记录；缺失或损坏时返回 `503 workspace_unavailable`，不会合成默认值。`PUT /workspace` 严格校验单个 JSON 对象、已知字段和值，在服务端事务内复核当前账号、平台管理员身份及全局 `workspace.manage` 授权，并返回持久化结果；`data_region` 与品牌素材由部署配置管理。尚无版本前置条件，保存的 slug、默认设置和留存天数也未驱动相关页面语义。角色/成员列表与详情只展示所选且已授权环境内的对象与成员；只有同时具备全局模块查看和全局 `environment.view` 的管理员可盘点无环境归属的旧记录。`GET/PUT /roles/{id}` 对平面五列矩阵无法无损表达的授权返回 `409 matrix_editor_required`；写入时在事务内复核当前有效账号、全局 `roles.manage` 与全局 `environment.view`，保护最后一位有效人类管理员。PUT 成功仅返回 `{ ok: true }`，不在提交后重读并泄露角色详情。这些后端门禁不表示整页设置或角色编辑已完成：前端仍使用示意数据，主契约维持 `mock`。

## 后端辅助接口（不计入上述 166 项）

- `GET /investigations/{id}/nodes/{node}/messages`：只读当前持久化调查分支的 `{ branch_id, revision, messages }`。服务端以分支记录核对调查、应用、环境和所属团队，再检查当前 `canvas.view`；消息保留历史 JSON 原貌，旧记录可能没有 `created_at`，不补造时间或 AI 摘要。当前上下文超过 24 KiB、损坏或缺失返回 503；不存在、路径不匹配或无对象访问权返回 404。完整调查图、节点写入和祖先回写仍为 `mock`。
- `GET /incidents/{id}/timeline/manual`：从持久化 `v3_incident_timeline` 读取当前事件的人工备注，响应 `{ items, scope: "manual_only" }`；按事件的应用/环境及当前 `events.view` 限定对象，按保存时间与 ID 排序，姓名取当前成员记录。未知或无权事件返回 404，超过 500 条人工记录或读取故障返回 503，不返回部分列表。不合成系统/AI 时间线、影响或错误预算，不能替代仍为 `mock` 的 `GET /incidents/{id}`。
- `POST /mcp-servers/{id}/discover`：在单一环境和当前 `connectors.manage` 授权下，对显式绑定的登记执行有界 MCP `tools/list`，保存不可变的**未信任声明观察快照**及管理审计。响应含快照 ID、登记版本/摘要、工具摘要/数量和采集时间，`trust=untrusted_observation`、`approved=false`、`health_verified=false`；不回传工具声明、端点或凭据。调用后再核对授权、登记与绑定实例；上游失败或声明无效时仅写脱敏失败审计并返回 502。发现不等于工具审核、启用、执行或健康证明，这些主接口仍为 `mock`。
- `GET /changes/records?service_id=…&binding_id=…&page=1`：只读已显式绑定的 GitLab 项目流水线，要求当前环境的 `services.view` 与 `connectors.view`，每页 20 条，最多查询第 1000 页；前后复核授权与来源。只返回可核实的流水线身份、状态、分支、提交及上游时间，不包含变更风险、告警关联或看板 KPI，不替代主 `GET /changes`。
- `GET /changes/merge-requests?service_id=…&binding_id=…&page=1&state=all&target_branch=…`：同一显式绑定的 GitLab 项目只读合并请求元数据；须单独启用 `list_merge_requests` 工具。每页 20 条，最多第 1000 页；返回分支、状态、提交标识及上游时间，不含差异、部署或风险推断。筛选非法为 422，来源或授权变化明确失败，不替代主 `GET /changes`。
- `GET /changes/commits/{sha}?service_id=…&binding_id=…`：只读绑定项目中完整 40/64 位十六进制 SHA 的提交元数据；须单独启用 `get_commit` 工具，`get_commit_diff` 不等于授权。只返回标题、时间和父提交标识，不返回正文、差异或作者邮箱，也不推断部署或风险。两接口的 `service_id` 均为应用 ID、NUL 和环境 ID 拼接后的 base64url；服务端按当前环境及绑定再核对。
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
