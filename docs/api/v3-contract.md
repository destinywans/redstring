# v3 前端 API 契约（Web ⇄ 后端）

- 状态：**契约冻结 v1（2026-09-28）**。前端已按本契约用 Mock 实现；后端按接口逐项迁移，`mock` 仍表示尚未达到后端完成标准，即使已有部分处理逻辑。历史：2026-09-24 初版；v4 增补模型与推理强度、附件、通用画布、各"新建"流程、链路 v2；2026-09-28 增补实例与环境。
- 表格每行只写一个方法和一个路径。`scripts/check-v3-contract.py` 会核对本文件和 Mock 路由是否一致，改契约后运行它。
- 冻结后的修改流程：先改本文件和 `types.ts`，再改 Mock 与后端；破坏性修改（删字段、改语义）在「变更记录」里写明。
- 冻结页面的类型真相源：`web/src/v3/api/types.ts`。本文件描述路径、参数和语义；本批后端安全字段（服务映射候选身份与修订）先由本文件及 OpenAPI 定义，前端类型/Mock 尚待同一契约适配，不能据此宣称页面已接通。
- Mock 实现：`web/src/v3/mock/domains/*.ts`，每个文件对应下面一节；`VITE_API_MODE=http` 时前端直接请求真实后端。
- 与既有 `docs/api-contract.md` / `internal/apicontract/openapi.json` 的关系：v3 为新 UI 所需的读模型。后端落地时优先复用既有资源（调查、分支、提案、执行、Runbook、连接器）；能直接对齐的路径就对齐，对不齐的在 OpenAPI 中新增，再同步 `scripts/build-openapi.py`。本文件的"状态"列在后端实现后改为 `implemented`。
- `scripts/check-v3-contract.py` 核对的 165 条 Mock 路由，加上前端模拟而无 Mock 路由的 SSE `GET /conversations/{id}/events`，共 166 条。`implemented` 只表示后端真实处理、范围鉴权和契约具备；不表示外部系统已实连或前端 HTTP 模式已验收。2026-09-29 起用户要求先交接口代码、不运行测试，本阶段新升状态只依据源码和必要编译核对，定向运行证据单独记录为未验证；此前各批已有定向证据不受此约定追溯改变。阶段进展与未完成项见 [后端实施记录](../plans/2026-09-28-v3-backend-implementation.md)。
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

服务端断线续传头 `Last-Event-ID` 只接受单个非负十进制事件 ID；空值、重复头、负数、非数字或溢出返回 `422 invalid_event_cursor`。正数游标必须属于当前有权会话，续传该事件所在的回复，即使之后已有新回合；已删除或未知游标返回 422。无正数游标时，可用 `reply_id` 选择本会话的指定回复，否则读取最新回复；两者同时提供时必须匹配。早期失败也先补空 `message.started`，再发状态与 `message.completed`。

当前后端在创建回合及成功或失败的结束事务中重核持久会话、所有者、环境和 V3 对话权限；租约恢复也重核，撤权后的未完成输出不会保存为成功回复。SSE 在连接和每批事件读取前重新核对当前查看权限；活动回复还核对原发起会话及 `conversation.change`，已完成回复按查看权限回读。隔离 PG17 注册 HTTP 测试已覆盖首批事件后撤销真实 Cookie 会话或修改权限、断线续传、终止游标与完成竞态。授权检查之后到事件查询或网络发送之间仍有并发撤权窗口，已经发出的字节无法撤回；这些检查不构成完整页面验收。

| 事件 | 含义 |
| --- | --- |
| `message.started` | 助手消息开始，携带空 `parts` |
| `part.added` | 新增一个富消息部件（`tool_calls`、`text`、`chart`、`table`、`citations`、`proposal`…，见 `Part`） |
| `text.delta` | 向第 `index` 个 `text` 部件追加 `delta` |
| `message.completed` | 结束，前端关闭 EventSource |

**按轮选择模型（v4）**：请求体可带 `model`（`provider:model`，来自 `GET /models`）与 `reasoning`（该模型声明的档位 id）；缺省时用 `GET /models` 的 `default`。一轮只用一个模型，回复进行中切换只影响下一轮。`message.completed.run` 仅在取得可信模型用量时出现；存在时才记录 `model_id`、`model`、`reasoning`、`reasoning_label`、`brand`，不能为缺失用量补零。冻结前端先经 `POST /uploads` 上传，再以 `attachments: Upload[]` 随消息提交。后端在接受事务中复核本人、当前环境、完整引用及内容，将粘贴和安全 UTF-8 文本文件（`.txt/.log/.conf/.toml/.md/.csv/.json/.yaml/.yml`）存为有界私有快照；符合安全 URL 规则的公开 HTTPS Markdown 链接（不超过 1000 字节、无查询/片段、只用 443 端口）在接受前安全抓取一次并冻结正文，重试及后续回合不重新抓取。PNG/JPEG/GIF/WebP 图片和 PDF 只交给明确支持相应媒体输入的 OpenAI Responses 模型，原始字节在同一事务保存为私有媒体快照；对外消息及 SSE 只返回附件引用，不返回正文或媒体字节。无效或越界引用返回 `422 invalid_attachment`，不支持的媒体/模型或不可公开读取的链接在建回合前明确 422。上传接口允许更宽的 HTTP/HTTPS URL，上传成功不保证链接可用于发送；内部 Grafana/Kibana/GitLab 链接也不能当作公开 Markdown 读取。超出来源、媒体或已知模型预算返回 413，上游链接暂时不可用返回 503；已接受回合的供应商错误保留失败状态并可重试。历史媒体及文本在后续回合按快照恢复，模型切换时也先检查能力与已知预算；媒体 token 成本和完整系统/工具输入仍以供应商为准，可能在接受后失败。迁移 `086_v3_conversation_attachment_context.sql` 不将旧引用推断为已读正文；`133_v3_conversation_media_snapshot.sql` 保存新媒体快照。冻结前端的粘贴展示别名现可按严格格式规范化；文件上传 XHR 仍缺环境头，页面附件发送与错误恢复未验收。`POST` 仅按后端处理、鉴权、契约和定向检查计为 `implemented`；SSE 按其独立读取语义判断，不表示真实供应商或整页已验收。

调查画布节点对话仍规划复用富消息部件与事件；当前通用画布节点 `POST /canvases/{id}/nodes/{node}/messages` 是持久化同步 `{ reply }`，不使用上述 SSE 流。

## 身份与全局（`identity.ts`、`home.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/me` | | `Me`；须配置当前权威值班来源 | implemented |
| PATCH | `/me` | `{ name?, title?, timezone?, preferences? }`；只允许本人可编辑字段 | `Me`；账号、偏好与审计原子保存 | implemented |
| PATCH | `/me/preferences` | `{ theme?, locale?, density?, reduce_motion? }` | 偏好 | implemented |
| GET | `/me/watched-services` | | 本人明确关注的当前有权服务；状态来源不足时 `status: null` 并标注 `availability` | implemented |
| GET | `/search` | `q` | 五类持久且当前有权对象的有界搜索；无真实最近访问记录 | implemented |
| GET | `/mentions` | `q`, `kind` | 服务、事件、调查、有效人员、本人对话的有界候选 | implemented |
| GET | `/home` | | 有界授权快照；无来源的 AI 简报、值班与服务健康可空，附 `availability` | implemented |
| GET | `/mentions/{kind}/{id}/preview` | | 当前有权五类对象的有证据预览；指标与关联未知为空，其他类别明确不可用 | implemented |
| GET | `/commands` | | `{ items: CommandItem[] }`；仅经配置和授权的斜杠文字建议，不执行命令 | implemented |
| GET | `/models` | | `ModelCatalog`：当前用户可用的模型、接口类型、推理强度档位与默认值 | implemented |
| GET | `/models/admin` | 单一已授权环境 | `ModelAdminView`；仅服务端明确绑定到该环境的模型、进程配置与持久历史使用；状态保持未验证 | implemented |
| POST | `/uploads` | multipart `file` + `kind`；链接与折叠粘贴用 JSON `{ kind, name, url \| text }` | `Upload`；单个 ≤ 20 MB，超出返回 413 | implemented |

`GET /home` 已接入按当前会话、环境及服务/事件/提案/对话权限过滤的持久记录聚合。服务登记数、未解决事件数和待复核提案数仅表示当前可见数据库记录，不表示外部平台健康或告警接收完整性；可选趋势只统计已入库且可识别等级的接收记录，未知等级不从关注列表删除。AI 简报、权威值班、服务健康、SLO 与建议没有可信生产者，保持 `null`、空数组与逐项 `availability`。冻结 `HomeSummary` 和首页会直接读取非空简报、趋势与健康字段，当前真实响应可能使页面报错；此处 `implemented` 只表示有来源部分的后端读取、授权、错误和可空契约代码完成，不表示页面或缺失生产者已验收。

`GET/PATCH /me` 按当前真人账号、有效 Cookie 会话及授权环境投影完整 `Me`。团队、部门、角色、偏好和本实例已登记的 TOTP/passkey 来自持久记录；MFA 字段只表示登记状态，不证明登录或审批时已完成二次验证。值班状态必须由运维配置的 HTTPS 权威来源对请求环境给出新鲜、完整且无歧义的回答，未配置或失效返回 503，不能把未知写成未值班。`PATCH` 只接受名称、职务、时区及支持的偏好字段，须选定单一环境；邮箱、电话、团队、角色、MFA 与值班均由服务端管理。账号、会话、授权与值班证据在写入时复核，资料、偏好和审计一起提交。后端接口已闭合；实际权威值班服务接入与前端整页验收仍需单独完成。

`GET /me/watched-services` 只读取本人显式保存的关注关系，不从负责人、团队、值班或订阅推断关注。当前环境与服务应用/所属团队权限决定可见项；无关注时返回真实空数组。最多扫描 500 条有效关系、返回 50 条可见项，超限明确 422。服务状态仅在当前有权指标、完整经审核告警覆盖和来源封印齐备时返回 `ok | warn | danger`，否则保留关注记录并返回 `status: null`、`availability.state: unavailable`、`complete_v3_dto: false`；这不表示 Kubernetes readiness。辅助 PUT/DELETE `/me/watched-services/{id}` 只更改本人关注关系及审计，ID 使用服务目录的应用/环境复合 ID；迁移 `140_v3_user_watched_services.sql` 未在现有库执行。冻结侧栏要求非空状态和 Mock 服务 key，页面需单独适配。详细边界见[关注服务](v3-watched-services.md)。

`AIOPS_V3_MODEL_BINDINGS_FILE` 为服务端无密钥配置文件；配置时启动即校验实例层级、全部环境与模型 ID，将同一绑定快照交给模型管理、`GET /models`、个人 AI 偏好、对话及画布执行。显式跨环境选模型返回 422；旧偏好不适用于当前环境时显示该环境绑定的默认模型，后台对话从持久环境复核后才调用供应商。`GET /models/admin` 从该环境绑定、进程适配器、只读工具启用名单及已持久的历史使用记录构建完整 DTO；无绑定配置、工具名单或不受前端 API 枚举支持的原生 Ollama 模型时返回明确 503。`status` 为未验证；`available` 仅指进程中有可用适配器与绑定，`used_by` 只表示历史记录，均不证明当前调用成功或 Agent 未来绑定。元数据辅助 `POST /models/check` 与 `GET /models/observations` 仍仅对无密钥、字面回环地址做一次有界查询，不发送提示词；其 `verified` 只表示该次元数据查询成功，不证明生成、流式、工具、图像或持续健康。

未配置绑定文件时，旧 V3 全局模型目录与执行路径为兼容既有实例继续运行，`/models/admin` 返回 `503 model_binding_unavailable`，不能声称具备按环境模型隔离。此模式只适用于管理员确认**该实例全部环境均可共用同一模型目录及进程级供应商凭据**的部署；不同环境需不同模型名单时必须配置绑定，不同凭据边界仍须分开实例或另建凭据路由，模型 ID 绑定本身不隔离同一进程中的供应商密钥。冻结模型页仍硬编码首选模型、检查成功提示和部分配置说明，真实供应商生成/页面交互未验收。

发现路由当前可按真实持久来源检索有权服务、事件、调查、有效人员与本人对话，支持中文与拼音，并分别将查询结果限制为 8 条搜索命中或 30 条提及候选；非空查询中任一来源类型匹配过宽时返回 413，不返回不完整成功结果。“最近访问”无真实行为记录，告警组、产物和 Runbook 预览无可用独立来源；仅支持五种引用类别，其他类型明确 422 或 503。预览指标和关联记录无来源时为空，不能解读为实时健康或无关联。调查结果暂链接画布列表，前端提及页签尚未按类型重新查询，引用发送的完整生产链仍待验收。

`GET /commands` 只读取私有 `AIOPS_V3_TEXT_COMMANDS_CONFIG` 中按环境登记的至多 32 项斜杠文字建议；文件必须为绝对路径、最多 16 KiB 的严格 JSON 数组，条目只允许 `conversation` 或 `canvas` 模块，并按当前用户该环境的 `change` 权限过滤。未配置返回 `503 commands_unavailable`，显式 `[]` 返回空列表；`all` 环境没有可发送建议。建议只向输入框插入字面文字，不能执行工具、Skill、Runbook 或模板。冻结 Composer 还有更多 `RefKind` 和命令分组，本阶段后端代码路径完成不等于整页已接通；真实前端、测试和浏览器检查均未进行。

### 实例与环境（`instance.ts`）

**一个平台实例只对应一个隔离边界。**生产平台独立部署：独立域名、数据库和凭据，界面上固定显示“生产”，不能切换。非生产平台管理开发、测试、预发和需求验证环境，可以切换，也可以选「全部非生产环境」查看。

- **跨环境读取**只读且单向：生产可以通过「低环境只读来源」读取非生产环境做对比，凭据存放在生产的 Vault；非生产平台不能访问生产。
- **配置提升**不在两个平台之间直接调用：非生产导出为配置仓库的合并请求，审核合并后，由生产平台在「配置同步」中导入。执行记录和证据不随之带过去。

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/instance` | | `InstanceInfo`：`tier`（`production` \| `nonprod`）、`name`、`domain`、`environments[]`（id、名称、分组、集群、回收时间）、`default_env`，以及另一个平台的链接 `other` | implemented |
| GET | `/integrations/lower-sources` | | `{ items: LowerSource[] }`，只有生产实例提供；非生产实例返回 404。仅返回服务端显式配置的来源元数据，不代表连接已验证 | implemented |
| GET | `/config-sync` | | `{ direction: "import" \| "export", items: SyncItem[] }`：生产实例列出已合并候选，非生产实例列出本人已导出项 | implemented |
| POST | `/config-sync/{id}/import` | `id=mr-<正整数 IID>`；只允许生产实例 | 验证已合并、签名且独立审核的四类配置并持久导入；不执行目标能力 | implemented |
| POST | `/promotions` | `{ kind: template\|runbook\|alert_rule\|agent_version, id }`；单一非生产环境 | `{ mr, path }`：净化快照签名后写入独立配置仓库并发起 MR；生产实例返回 409 | implemented |

`GET /config-sync` 在非生产列出当前操作者有权导出的签名记录，在生产仅向当前人类平台管理员列出已合并的候选；四类 `template`、`runbook`、`alert_rule`、`agent_version` 均按目标记录、导入台账和成功审计核对后可显示 `imported`。两端均核固定提交、唯一文件、签名、当前来源版本和权限，最多返回 100 项；超限 413，仓库或信任未配置 501，来源无法核实 503。`merged` 表示此刻可验证的合并状态，不证明合并当时的审批或分支保护。冻结页面只在 `pending` 展示导入入口，而生产读取返回 `merged`，页面未验收，详见[配置提升证据边界](v3-config-sync.md)。

`POST /promotions` 在单一非生产环境对已净化模板、当前已发布 Runbook、精确绑定的 Prometheus 告警规则或当前稳定 Agent 版本读取受权来源，记录版本、环境、应用、净化摘要及 Ed25519 签名，再向独立 GitLab 配置仓库创建 MR；仅模板保留净化图结构，其他类型不携带原始正文、工具、连接器、注解或运行态。告警规则来源复用正式 V3 的同一受审规则组绑定；未配置时该种类明确不可用。`POST /config-sync/{id}/import` 只在生产以当前人类平台管理员和全局 `workspace.manage`，核已合并 MR 的固定提交、唯一预期文件、来源签名、独立审批与当前目标分支保护，并在写入事务内重核授权及保存目标、台账和审计。Runbook 导入为待审核草稿，告警为 `not_applied` 记录，Agent 不改变现有发布状态；导入不执行 Runbook、不应用规则、不切换 Agent 流量。配置、签名或仓库未接返回 501/503；既有同一导入收敛，目标漂移或冲突返回 409。GitLab 当前 API 不能证明**合并当时**的审批/保护状态，真实配置仓库上线仍需部署治理证据；迁移 125 与真实仓库/页面本轮未验收。两条仅按本阶段有条件的真实后端成功路径计 `implemented`。

原有已净化模板在隔离 schema 与替身 GitLab 的往返及新连接读回，只验证当时的模板持久化；新增 Runbook、告警规则、Agent 三类写入、迁移 125 和真实 GitLab 仓库尚无本轮运行证据。重复导入同一变更应收敛为一条台账和成功审计，目标或台账唯一冲突返回 409；不要把来源分支既有测试当成本集成组合的实测证明。每来源环境 ID 的独立允许列表尚未配置。

当前环境保存在浏览器本地（`aiops-v3-env`），并写入地址栏 `?env=`，这样分享链接时打开的是同一个环境。

主题、语言、密度、减少动效保存在浏览器本地（`aiops-v3-preferences`），并尽力同步到 `PATCH /me/preferences`。

## 对话、画布与模板（`conversations.ts`、`canvases.ts`、`canvas.ts`、`templates.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/conversations` | `filter`, `q`, 分页 | 四标签页同一授权快照中的 `ConversationList`；未知用量与来源为 `null` | implemented |
| GET | `/conversations/{id}` | | 本人对话或私人通用画布的持久 `Conversation`；纯调查 ID 用调查图读取，返回 404 | implemented |
| POST | `/conversations` | `{ text, context?, model? }` | `{ id }` | implemented |
| POST | `/conversations/{id}/messages` | `{ text?, model?, reasoning?, attachments? }`；非空文本或至少一项附件 | `{ message, reply_id }`，随后由事件流推送回复 | implemented |
| GET | `/conversations/{id}/events` | `reply_id?`、`Last-Event-ID?` | SSE：`StreamEvent`（见「流式对话」）；回复结束后服务端保存完整消息 | implemented |
| GET | `/canvases` | `kind`, `q`, 分页 | `{ items: CanvasSummary[], counts }`；`kind`：investigation / plan / review / analysis / custom | implemented |
| POST | `/canvases` | `{ title, kind, template_id, goal, services, ai_draft }` | `{ id, href }`；调查类返回 `INV-*` | implemented |
| GET | `/canvases/{id}` | | `CanvasDoc`（非调查类画布：通用节点块、边、产物、各节点对话） | implemented |
| POST | `/canvases/{id}/nodes/{node}/messages` | `{ text, model?, reasoning? }` | `{ reply }` | implemented |
| GET | `/canvas-templates` | | `{ items: CanvasTemplateOption[] }`；系统空白模板与当前本人有权环境中的私有通用画布模板，超过 500 项受控 422 | implemented |
| POST | `/canvas-templates` | `{ from_canvas, name, keep: { nodes, steps?, runbooks?, content? } }`；`nodes` 必须显式给出，其他保留项为 true 时 422 | 本人私有通用画布结构模板 | implemented |
| GET | `/investigations/{id}/canvas` | | `CanvasGraph`；持久分支、上下文修订、证据及布局，未知 token 用量为 `null` | implemented |
| POST | `/investigations/{id}/branches` | `{ parent_id?, parent?, title, x?, y?, expected_parent_revision? }`；至少一个父 ID | `CanvasNode`；锁定父修订后复制上下文，分支、布局、事件、审计同事务 | implemented |
| PATCH | `/investigations/{id}/nodes/{node}` | `{ x?, y?, collapsed?, title?, expected_layout_version, expected_title? }`；改标题必带 `expected_title` | `CanvasNode`；版本冲突 409、缺前置条件 428 | implemented |
| POST | `/investigations/{id}/nodes/{node}/messages` | `{ text, model?, reasoning? }` | `{ summary }`；当前回复不会自动派生子分支 | implemented |
| GET | `/investigations/{id}/writeback-preview` | `node` 或 `from`，可选严格祖先 `target` | 当前分支可选助手消息、目标修订与历史时间线；预算未知为 `null` | implemented |
| POST | `/investigations/{id}/writeback` | `{ node 或 from, target, items 或 conclusions, mode, expected_source_revision, expected_target_revision }` | `{ version, source_revision, target_revision }`；修订冲突 409、缺前置条件 428 | implemented |
| GET | `/investigation-templates` | `owner?=all\|official\|team`, `q?` | `{ items: InvestigationTemplate[], counts: { all, official, team } }`；空目录返回空列表 | implemented |
| POST | `/investigation-templates/{id}/start` | `{ inputs: { application_id, template_version, request_id } }` | `{ investigation_id }`；新建 201、同用户同参数重试 200 | implemented |

对话详情、后续模型上下文和最新回复使用持久 `sort_order`，同一事务写入的用户/助手消息按回合排序。有 `accepted` 审计的回合按序列化顺序；迁入或无审计的旧回合只按时间戳作确定性回填，不能据此证明原始提交顺序。迁移 085 在应用启动事务中回填整表并建索引，部署前须停旧回复 worker 并在代表性副本量测锁时。

回复事件先持久化，再推进进程内文本和部件缓冲；租约中断后按事件序列恢复文本与已持久的非文本富部件，保留多个状态、工具和图表部件的原顺序，再保存失败状态。最终模型文本只在剩余 `text.delta` 持久化后进入完成消息；指标来源会话在这些事件与结束事务前继续复核当前来源授权。本阶段此修复仅完成源码集成与最小编译，未重跑恢复测试或真实模型、数据库联调。

`GET /conversations` 在同一当前会话、角色、环境和对象授权快照中合并持久私人聊天、本人通用画布与应用调查。`mine` 使用持久所有者/创建者，`team` 使用当前应用团队成员资格，`triage` 要求关联调查的已持久自动初查运行；初查运行只证明发生初查，不推断调查创建来源。四标签、搜索、分页、总数与计数由同一可见集合计算。未知供应商用量、历史来源与无法映射的调查状态保持 `null`，不把部分数值推成完整值。`GET /conversations/{id}` 在同一读取快照复核精确会话、环境、对象所有者及查看权；私人聊天返回持久顺序的富消息和附件引用，运行/失败状态按已保存事实显示。通用画布交换以 `message.node_id` 关联节点，仅保留有证据的文本、模型和状态；不补造富消息或用量，纯 `INV-*` 无线性对话记录时返回 404，须读调查图/节点消息。历史超过 1000 条消息或 8 MiB 返回受控错误，不截断。冻结前端将列表 `tokens`、状态和详情 `context.used/budget` 当非空数值，调查条目入口亦依赖画布适配；两条仅按后端完成计入，页面仍未验收。

`GET /canvases` 在同一数据库快照中合并本人私人通用画布与当前应用/团队、环境有权的持久调查，按 `kind`、搜索和分页给出列表与完整计数。调查状态由持久 `open/closed` 映射；历史调查无记录的创建者返回空参与者数组，不编造人员。`POST /canvases` 在单一环境中创建通用画布，调查类要求恰好一个有效且已映射的服务；保存时重核当前 Cookie、画布权限、服务及负责人团队，画布/调查、初始节点与审计共同提交。可选 AI 草稿只用用户标题、目标和服务 ID，拒绝工具及非结构化结果；模型不可用则 503 且不创建。列表中的调查链接不代表完整调查详情已接通；冻结新建页会发送三个固定服务，调查创建会返回 422，页面仍待适配。

通用画布模板 GET 在同一有界快照中重核精确会话、当前环境与 `canvas.view`，只列系统空白模板和本人有权环境中的私有结构模板；超过 500 项明确返回 422，不截断。POST 在同一事务复核会话、本人同环境源画布和当前 `canvas.change`，`keep.nodes=true` 只复制净化后的节点/边布局，false 保存空结构，均排除源节点标题、内容块、产物与对话；按本人串行化重复名称与容量检查，模板和审计原子提交。系统模板的独立导入尚不共用该容量锁，可能使列表达到上限，后续 GET/POST 将受控 422；统一容量策略留 WAN-124。模板名称仍是可持久化文本：当前前端名称留空时会沿用源画布标题，可能包含需用户检查的业务信息。前端默认提交 `steps=true`、`runbooks=true`，而后端明确 422；页面还称“团队模板”，但当前仅为私有模板，列表不含调查模板。两条路由只按后端接口标准计入，页面未验收。

通用画布节点 `POST /canvases/{id}/nodes/{node}/messages` 的后端已接持久化同步回复：限定本人画布、单一环境和当前 `canvas.change`，一次只运行一条回复。模型上下文包含当前节点及沿有向边可达的祖先节点块与已完成回合，不包含兄弟或失败回合，超过 24 KiB 返回 413；工具调用被拒绝。运行期间约每 2 秒检查当前会话与授权，失败时取消模型请求；完成事务再次核对原会话、画布归属与当前 `canvas.change`，完成时无权限则不保存助手回复。若撤权又恢复全部落在两次检查之间，不能保证取消本轮。失败的用户输入保留供刷新核对，不生成虚假助手消息。`GET /canvases/{id}` 校验节点、七种块、边和产物结构，在 `threads` 返回各节点完整已保存回合；历史超过 500 次交换、2 MiB 总量或单条助手消息 64 KiB 时返回 413，不截断。写入仍可继续，故达到上限后详情会持续 413，当前没有分页或归档恢复。两条主路由按后端判据标为 `implemented`，仅适用于私人通用画布；调查节点接口仍分开。前端尚无超限恢复/分页，发送失败也未清除 `busy`，真实 409/413/422/503 可卡住输入框，因此画布整页尚未验收。

调查画布按持久调查、分支、修订、证据和节点布局工作：读取在同一事务中核当前应用、环境、负责人团队及 `canvas.view`，节点/修订/证据分别超过 100/1000/100 条或单条展示证据超过 16 KiB 返回 413，不截断为貌似完整的图。分支派生要求单环境、开放调查及 `canvas.change`，锁定父分支并复制其当前修订；节点修改用布局版本比较，标题另与当前标题比较。成功写入与事件、管理审计原子提交，两个写接口均不调用模型；入口和提交前按原始 Cookie 复核精确会话及当前授权来源。历史运行的 token 用量不是当前上下文用量，因此返回 `null`；没有可信调查循环状态时 `loop` 为空。

调查节点消息在持久分支中保存运行 Attempt，然后只向环境允许的模型发送该分支创建时已继承的上下文和本次文本；不读取实时祖先或兄弟分支，不启用工具。成功时在一个事务中追加用户/助手消息、上下文修订、事件与审计；模型失败或撤权只保留失败尝试，不伪造助手结论。`GET /investigations/{id}/nodes/{node}/messages` 辅助路由可读当前修订及最近尝试。预览只从当前来源分支中选尚未回写到目标的持久助手消息，不生成摘要或推荐；省略目标时选根祖先。回写必须显式指定严格祖先目标、当前来源与目标修订和选中的消息 ID；`summary` 模式把历史助手文本标成不可信用户上下文，`messages` 模式原样复制，`evidence` 模式要求已有证据引用。写入目标的新修订、事件、审计原子提交，不修改来源或基础设施；重复来源消息、陈旧修订和超 24 KiB 上下文均显式拒绝。冻结画布页仍假定固定根节点及数值 token，拖拽缺布局版本，回写缺目标与双修订，预览预算字段按数字显示，整页尚未验收。

调查模板是单独配置的持久、只读大纲，迁移不预置示例。GET 在同一有界事务中按当前会话、环境、应用映射、团队成员和 `canvas.view` 及引用权限过滤，`owner` 与 `q` 过滤条目，`counts` 统计完整可见集合；超过 500 个候选时返回受控 503，不截断。POST 只接受单一环境以及列表版本、应用 ID 和用户生成的请求 ID；启动前复核 `canvas.change`、团队成员、已发布且应用范围匹配的 Runbook、已人工审核的只读 MCP 工具与对应查看权限。在同一事务中保存调查、根与模板子分支、空上下文、冻结大纲及预算快照、事件与审计，不启动 Agent、模型、Runbook 或工具。同用户同请求 ID 的匹配重试返回已有调查，参数冲突返回 409；模板/依赖变更或撤权不会产生新调查。当前冻结前端启动按钮不发送 `inputs`，且模板列表未提供直接选择应用的字段，真实模式的启动交互需单独适配；本批仅按后端标准计入。

## 事件与告警（`events.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/inbox` | `filter: all\|mine\|mentions\|subscribed` | 当前授权的持久告警事件；已读及未接成员关系为 `null`/明确 503 | implemented |
| GET | `/alerts/{id}` | | 当前授权的持久告警详情与本地通知台账；无精确发生记录来源的字段为 `null` 并列入 `unavailable` | implemented |
| POST | `/alerts/{id}/claim` | `{ expected_revision }`（取自详情读取） | 按当前修订原子认领、审计并返回更新后的持久告警详情；旧修订返回 412 | implemented |
| GET | `/alert-groups` | `q`, `severity`, `env`, `state`, `show_inhibited` | 按持久事件本地聚合；指标、抑制及外部投递未知 | implemented |
| GET | `/alert-groups/{id}/policy` | | 明确来源绑定下读取当前 Alertmanager 策略；缺来源时可空且列出 `unavailable` | implemented |
| POST | `/alert-groups/bulk` | 单环境、1–20 个当前 GET 分组 ID、`action: claim\|silence\|investigate` | 逐组 `200/207` 结果；认领与调查写入本地持久状态，静默逐项 `not_connected` 且不创建提案 | implemented |
| GET | `/services/{id}/peek` | | 当前有权服务的持久身份/档案；健康、KPI、值班和变更未知字段可空 | implemented |
| GET | `/people/{name}/peek` | | 当前有权真人账号与资料职务；值班和负载未知字段可空 | implemented |
| GET | `/incidents` | `status: active\|resolved\|all` | `{ items, counts }` | implemented |
| GET | `/incidents/{id}` | | 当前授权的持久事件及人工时间线；时长、影响与系统/AI 时间线未知 | implemented |
| POST | `/incidents/{id}/timeline` | `{ text }` | 时间线条目 | implemented |
| GET | `/alert-rules` | | `AlertRuleSummary[]` 与完整可见范围的 `all/firing/scheduled` 计数；仅展示服务端按 Prometheus 规则文件和组明确绑定到应用、标签和环境均匹配，且当前应用或其负责团队有 `alert_rules.view` 的规则；上游读取后复核负责人和授权，范围变化不返回旧列表。来源失败或超限明确报错，不返回部分计数；无可信周期来源时 `scheduled=0` | implemented |
| GET | `/alert-rules/{id}` | | 当前受权 Prometheus 规则详情；编辑、通知和 AI 字段未知时可空 | implemented |
| POST | `/alert-rules/{id}/dry-run` | `{ expr, for }` | `{ would_fire, windows }` | implemented |
| PUT | `/alert-rules/{id}` | 单环境、`Idempotency-Key`、`{ expected_version, observed_rule?, rule: { application_id, environment_id, group, name, expr, for, labels, annotations, mode: draft } }` | 当前受权且来源未漂移时导出新版本的不可部署 JSON 草稿，返回 `pending_review` 与经核对的 GitLab MR；不启用规则 | implemented |
| GET | `/notification-routes` | 单一所选环境；需环境级 `alert_rules.view/manage` | 当前生效 Alertmanager 环境子树、`config_revision`/ETag、匿名接收方引用；频道目标、命中数及值班未知 | implemented |
| GET | `/silences` | `state: active\|planned\|expired\|all` | 静默列表及当前可见记录计数；应用标签需在服务端显式绑定到当前环境的有效应用 ID，再按应用/团队授权判断；环境明确但应用范围较宽的静默仅环境级静默管理员可见；无精确环境或匹配器歧义的静默不展示，宽范围只报告覆盖标志 | implemented |
| GET | `/inhibitions` | 当前所选环境与应用/团队可见范围 | 当前 Alertmanager 抑制配置的受权子集；匹配值脱敏，`active`/`hits_7d` 未知，返回 `206 partial` | implemented |
| POST | `/silences/preview` | `{ matchers: [{ k, op, v }] }`；单一环境 | `{ affects, sample }` | implemented |
| POST | `/silences` | 匹配器 + 时长 | `{ id }` | mock |
| DELETE | `/silences/{id}` | | 204 | mock |
| GET | `/proposals/{id}` | | `Proposal` | implemented |
| POST | `/proposals/{id}/approve` | `{ comment? }`（需通行密钥二次验证） | `{ execution_id }` | mock |
| POST | `/proposals/{id}/reject` | `{ comment? }`；仅待审提案，由人类身份执行 | 更新后的提案 | implemented |
| POST | `/proposals/{id}/comments` | `{ text }`；可选 `Idempotency-Key` 请求头 | 评论 | implemented |
| GET | `/executions/{id}` | 当前有权的持久执行 ID | 旧动作执行、审批、事件、可得恢复观察及结案报告的来源投影；V3 提案关联与图表未知 | implemented |
| POST | `/executions/{id}/report/publish` | 单一环境、当前真人会话，无请求体 | 审批摘要、恢复验证、结案报告均匹配时写本地发布台账与审计；不外发报告或再次执行 | implemented |

两条悬浮卡 GET 从当前有权的持久服务/真人目录读取 ID、名称、环境及可得档案；服务 key 或应用 ID 在当前可见环境中不唯一、同名人员不唯一时返回 409，不任取一条。服务健康、KPI、当前值班、变更及人员负载尚无完整授权来源，显式可空或空数组；资料职务不是 RBAC 角色。冻结悬浮卡会把未知状态显示为正常或非值班，页面需单独适配；字段证据与差异见[悬浮卡来源](v3-peeks.md)。

`GET /inbox` 仅聚合当前有权、今天与昨天更新的非疑似持久告警事件；`all/mine` 为此窗口中的可见记录数，`mine` 按当前负责人判断。已读回执、提及、订阅和其他收件类型没有可信成员账本，相关值保持 `null`，`mentions/subscribed` 筛选返回 503；超过 5000 个候选明确失败，不把不完整总数当完整。`GET /incidents/{id}` 在当前归属与会话授权下读取持久告警事件或人工确认的独立事件及至多 500 条人工备注；人工事件的告警关联从持久关联表读取。告警开始不推断处置时长；指挥官、阶段、影响、系统/AI 时间线及跨对象链接无来源时保持 `null`、空值和 `unavailable`。新增人工事件表在真实读取前须迁移，本阶段未执行现有库迁移。

`GET /alert-groups` 只按当前授权的环境、应用、Webhook 来源与指纹聚合持久事件，`count` 是本地接收次数，`notify` 只代表平台队列状态，不是原生 Alertmanager 分组或最终送达。未知指标、静默/抑制与外部状态保持可空，`state=silenced` 和 `show_inhibited=false` 明确 503；超过 5000 个候选不返回截断总数。`GET /alert-groups/{id}/policy` 仅在历史事件保存同一组真实标签，且私有配置的 `source_ids`、环境标签与应用映射均与当前持久范围一致时读取固定环境 Alertmanager `/api/v2/status`，返回继承后的分组/时序和接收者名称，不返回原始配置或凭据；读取后重核组成员与授权。未配置或历史标签缺失时返回可空策略与 `unavailable`，已配置来源失败或范围错配则失败关闭；时间线仅为本地通知队列。`ops_incidents.labels` 的启动迁移尚未在现有库执行，历史记录不会自动补标签。四条路由此处只按后端有来源的可空/不可用契约计为 `implemented`；冻结前端仍要求已读、分组指标、策略和事件影响等字段非空，真实页面未验收。

`POST /alert-groups/bulk` 使用 GET 相同的环境记录 ID、应用、来源和指纹计算分组身份，单环境一次处理 1–20 个 ID，锁定当前精确成员并逐组复核真人会话、应用或团队事件修改权。`claim` 仅更新平台认领人，`investigate` 还要求画布修改权并创建或复用持久调查和根分支；重复操作返回已完成结果。每个 ID 有单独结果，部分失败返回 207。`silence` 当前恒定逐项 `not_connected`：已有应用/告警名匹配器无法证明只静默选中来源与指纹的分组，因此不创建提案或上游静默。该主路由只按认领/调查的真实条件成功路径计 `implemented`，静默动作及冻结前端的批量请求/结果提示未验收；见[批量分组动作](../../internal/v3events/README-bulk.md)。

`POST /alert-rules/{id}/dry-run` 仅对当前环境中唯一绑定且有权查看的规则运行最近六小时 Prometheus 历史 `query_range` 采样模拟。请求 `expr` 必须与服务端当前规则表达式逐字一致，`for` 可在 0 到 1 小时内调整；规则、负责人、范围与授权在上游查询后复核。每条返回序列必须带精确的应用和环境标签，缺失或混合范围、数据不完整与超限均拒绝，不返回虚构计数。`would_fire` 和 `windows` 是满足持续时长的采样片段，不是历史通知或实际告警状态；`query_offset` 与 `keep_firing_for` 未复现，规则表达式及查询输入范围仍须由可信配置和部署隔离保证。冻结页面发送 `threshold/for_minutes`，与此请求体不符；真实 Prometheus 及页面尚未验收。

`GET /alert-rules/{id}` 从当前唯一绑定、受权的 Prometheus 规则文件读取名称、表达式、标签、注解、持续时间与可得求值时间；编辑器草稿、阈值预览、通知路径、Runbook 和 AI 初查没有该规则的可信绑定时为 `null` 并列入 `unavailable`。`GET /notification-routes` 只返回能证明由所选环境独占的当前 Alertmanager 路由子树，以配置摘要提供 ETag；接收方名称、目标、凭据、命中数、值班和升级均不泄露，接收方只用该修订内的匿名引用。`POST /notification-routes/test` 只接受当前所选单环境下、服务端精确绑定应用及环境标签的样例告警；具备环境级规则查看与管理权后，对当前生效策略返回命中节点路径、匿名接收方引用和继承后的分组时序，不发送通知，也不试算未保存草稿。条件时段或共享路由歧义失败关闭；冻结页面的草稿试算、接收方名称及频道目标仍未验收。这三条路由仅按后端有来源与明确未知字段计为 `implemented`，详见[通知路由草稿契约](v3-notification-route-drafts.md)。

`GET /executions/{id}` 从持久旧动作执行、计划、审批、调查事件与已结案报告读取当前有权事实；无持久 V3 提案关联时 `proposal_id: null`，图表、阈值、影响、编辑时间线和行动项没有可信来源时为 `null` 并列入 `missing`。`POST /executions/{id}/report/publish` 要求当前真人会话、提案查看与知识管理授权、审批摘要和执行结果相符、已存恢复验证及结案报告/事件证明；仅将报告摘要和操作者写入本地发布台账与审计，同摘要重试返回原记录。V3 旧动作 Worker 关闭，当前后端仅能投影已有持久执行；这两条路由不建立新的 V3 恢复执行链，也不把报告发往外部。冻结执行页要求非空提案、图表和展示字段，未作页面验收；领域边界见[执行报告契约](../../internal/v3executions/README.md)。

`POST /silences/preview` 按所选单一环境读取实时 Alertmanager 告警快照。请求支持 `=`、`!=`、`=~`、`!~`（1–20 条）；环境是隐含的精确匹配。精确 `application=` 值必须在服务端绑定到有效应用并有当前应用/团队 `silences.view`，无应用匹配则要求环境级 `silences.view` 与 `silences.manage`。Alertmanager 读取后再次核对当前授权和应用归属。`affects` 是此刻该环境匹配告警的完整条数，`sample` 仅为最多 5 个去重排序的真实 `alertname`；来源异常、超过 500 条该环境告警或异常 `null` 均返回 503，不给部分计数。预览不会创建静默。现有页面默认仅传 service/alertname，普通应用用户会因缺精确应用匹配收到 403，且页面尚未正确清除旧预览/失败状态；后端路由已闭合，页面和真实 Alertmanager 尚未验收。`GET /alerts/{id}` 从来源绑定的持久事件及本地通知传输台账返回有证据字段，来源、规则、曲线、分组、Runbook 等缺精确发生记录关联时为 `null` 并列入 `unavailable`；`source_id` 只标记入站身份，不证明当前上游健康。`POST /alerts/{id}/claim` 以 GET 的事件行修订作前置条件，事务内复核人类会话与查看/修改权、更新认领与审计，再返回修订后的详情；冻结前端仍传空请求体并假称外部升级已停止，页面未验收。

底层 Alertmanager 适配器可按 v2 协议创建和过期静默：创建不带客户端 ID，校验返回 UUID；过期使用单数 `/silence/{id}` 路径；超时等结果不确定的写入单独标记。创建台账现能持久保留当前有权人类操作者的环境/应用作用域、固定载荷、幂等键和审计；另有仅供受信进程内调用的静默提案生产者，将同环境未关闭调查、当前应用/团队和权限核对后的固定动作快照、摘要及供人阅读的内容原子保存，并不创建上游静默或开放产品写路由。提案的查看、评论、驳回在事务内复核当前权限；驳回仍须人类身份。私有对账会枚举一个配置端点的完整静默列表：零条标记保持未知且不重发，唯一完整匹配才确认，重复或失配进入人工复核；过期时间窗会在保留后执行与低层发送前拒发。单端点快照不能证明 HA 集群最终收敛后的全局唯一。生产执行前仍须在同一事务核验具体持久提案及人工批准，并由独立执行器按明确的未发送/不确定结果路径调度。删除还缺对象归属、授权和持久意图；反向代理的 404 不能证明对象已删除。未注册 `POST /silences` 或 `DELETE /silences/{id}` 产品路由，两条主契约继续 `mock`。

`GET /inhibitions` 仅展示来源/目标都绑定当前环境、应用归属有效且当前用户有相应应用或团队 `silences.view` 的规则；宽应用范围要求同环境的查看与管理授权。跨环境、匹配器歧义与失效绑定不会出现在列表；任一已授权上游来源失败时返回 503，不给部分规则。成功响应始终为 `206 partial`，匹配值脱敏，逐规则活动状态与命中数为 `null`；当前有来源、授权、错误和明确未知字段的后端契约计为 `implemented`，不代表完整活动状态或页面验收。见[抑制规则与草稿契约](v3-inhibition-drafts.md)。

## 知识与记忆（`knowledge.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/runbooks` | `q`, `service`, `tab`, `page`, `per_page` | 当前授权的持久 Runbook 列表；用量、成效和建议未知时可空 | implemented |
| GET | `/runbooks/{id}` | | 当前授权的结构化 Runbook；用量、事件关联与执行核验未知时可空 | implemented |
| POST | `/runbooks/{id}/dry-run` | 当前对象需 `knowledge.manage`；验证持久化步骤并保存预览/审计，不执行外部动作 | 演练结果 | implemented |
| POST | `/runbooks/{id}/submit` | 当前持久化草稿，无请求体 | 提交审核 | implemented |
| GET | `/postmortems` | `tab?`, `q?`, `page?`, `page_size?` | 当前受权的持久复盘及有来源的结案时长；其他趋势、影响与跟进未知 | implemented |
| GET | `/memory` | `service?` | 当前受权、发布来源锚定的事实与真实冲突；置信度、召回和过期未知时可空 | implemented |
| POST | `/memory/facts/{id}/{action}` | `action: confirm\|reject\|expire` | `{ fact, fact_redacted, partial, unavailable_fields }`；清理旧来源时事实正文脱敏 | implemented |
| POST | `/memory/conflicts/resolve` | `{ keep, drop }`；均为真实事实 ID | `{ id, state, keep, drop }` | implemented |
| GET | `/knowledge-sources` | | 当前授权登记与经证明的手动读取；连接、持续同步、分块和索引未知，完整可空证据 DTO | implemented |
| POST | `/knowledge/search` | `{ q, service?, top_k? }` | `{ hits: RetrievalHit[] }`；关键词与向量共用当前显式环境、有效应用绑定和 V3 授权过滤，团队标记另需匹配当前负责人及团队查看权限；输出前按文档 ID/版本重核。历史归属不明、异常元数据和未验证登记来源不进入结果；独立存储的公开 Web 正文仅在最新观察、哈希、修订和当前授权一致时进入有界关键词片段，全部可见应用/环境合计超过 80 个数据库匹配来源返回 413；整次搜索限时 30 秒 | implemented |

知识治理另有证据与人工审核辅助接口，详见 [V3 后端辅助接口](v3-backend-aux.md)。`GET /knowledge-sources` 与辅助 `registered-evidence` 读取当前有权来源登记和最近经版本、正文哈希、作用域及读取观察核对的证据；Git Markdown、Confluence、Notion 和飞书 Docx 必须仍匹配当前精确服务端绑定，本人上传须仍有原文件。主 GET 现提供完整的可空证据 DTO，`partial: true` 描述证据覆盖范围，不表示响应缺字段；连接健康、持续同步、片段与向量索引仍未知，不能从零或空值推断健康。最终返回前复核会话、来源和授权；前端仍假定确定连接与索引值，页面未验收。`GET /runbooks` 在同一事务快照中锁定当前会话与角色授权来源，逐项核文档环境、应用、团队及对象权限后过滤、计数与分页；至多接受 10000 条可见匹配。未记录核验时间时 `stale` 为 `null` 且计入 `verification_unknown`，引用、处置率、KPI 与建议保持可空。`GET /runbooks/{id}` 在同样的授权快照中读取结构化步骤、修订与已记录批准：隐藏或非 Runbook 为 404，有权旧文档缺结构化记录为 `503 structured_steps_unavailable`；用量、事件关联、待审人员与执行核验缺可信来源时返回可空字段及 `unknown_fields`，不补零。两条主 GET 均只按本阶段后端真实来源与诚实未知口径计为 `implemented`，`complete_v3_dto=false` 和 `partial=true` 明确冻结页面 DTO 未闭合、页面未验收；`last_verified_at` 仅是记录时间，不证明执行成功。`GET /memory` 现从当前有权、仍发布且版本/正文锚定的持久事实投影服务与工作区计数及双方可见的人工记录冲突；`confidence`、`recalls`、`expires`、`policy` 为 `null`，不推断工作、情景或语义召回。超过有界事实/冲突数返回 413，来源或授权不可用返回 503；冻结页面仍按非空 Mock 类型渲染，需另行适配，详见[记忆主视图证据边界](v3-memory-view.md)。事实状态操作仅支持 `confirm`、`reject`、`expire`，冲突裁决须传入两个真实事实 ID；写入在事务中复核会话、当前授权、来源版本与对象归属并保存审计。清理陈旧来源的 `reject/expire` 回包脱敏，`confirm` 和冲突裁决要求当前发布来源。两条写路由按后端接口标为 `implemented`；现有前端的 `forget`、冲突请求体和整个记忆页仍未对齐，`GET /memory` 继续 `mock`。

`GET /postmortems` 在当前会话、环境、事件及知识权限下读取持久复盘、对应旧事故和实际结案事件；可计算可见复盘数及有结案证据的时长。根因类别、跨服务影响、行动完成率、MTTA 与趋势缺来源时列为 `unknown_fields`，空集合不表示测得为零；超过 1000 条可见复盘返回 413。它与草稿 GET 只按后端来源、权限和诚实未知字段计 `implemented`，`complete_v3_dto=false`，冻结仪表盘仍未验收。新建的独立人工事件尚未具备解决转移和复盘候选路径。

V3 知识正文读取按持久环境、有效应用与当前授权失败关闭；带 `team_id` 的文档还要求该值与应用当前负责人一致并有团队查看权限。历史 `knowledge_documents` 中标记了 `source_registration_id` 的来源文档在来源证明链闭合前不参加 V3 检索或来源投影。另设的公开 Web 正文/修订表只接受有持久应用归属、无凭据的单页 HTTPS Markdown 同次抓取；关键词检索在候选及输出前复核最新观察、内容哈希、修订和当前环境/应用/团队授权；不进入旧文档详情/导出或向量索引。可选的 `search_knowledge` Agent 证据组件尚未在正式进程注册；V3 实例关闭旧 Worker，不能把旧检索工具当作当前 V3 调用路径。其他来源正文同步、索引归属与完整页面 DTO 仍待接入。

## 资源与观测（`observe.ts`）

只读聚合；遥测数据按需从连接器查询，不落平台。

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/services` | `q`, `tier`, `team`, `status` | `ServiceCatalog`；仅完整来源、当前有权服务，缺项返回 503 | implemented |
| GET | `/services/{id}` | | `ServiceDetail`；按持久服务身份汇集指标、告警、发布、依赖与当前调查 | implemented |
| GET | `/services/{id}/resilience` | | `Resilience`；仅在固定 HTTPS 五类完整来源配置且证据有效时返回 200，否则 503 | implemented |
| POST | `/services/{id}/resilience/proposals` | `{ kind, params: { investigation_id, reason, desired } }` | 201 `{ proposal_id }`；仅保存人工审阅提案，不创建可执行动作或合并请求 | implemented |
| GET | `/topology` | `cluster`, `around`, `depth`, `group_by` | 无 `topology_v1` 目录项时为授权 CMDB 部分响应；配置经审查的精确来源后可返回完整 `Topology`，来源不全返回 503 | implemented |
| GET | `/metrics/dashboard` | `service`, `range=1h`, `align?` | `MetricsDashboard`；单环境、单服务、唯一持久 Prometheus 目标和经审查的精确范围查询，完整面板与来源核对后返回 | implemented |
| POST | `/metrics/ask` | `{ service, view, range, question, request_id }`；`service` 为持久应用 ID，`view=request_rate\|http_p95_seconds`，`range=15m\|1h`，`request_id` 是 16–128 字符重试标识 | 首次接受为 201 `{ conversation_id }`；同一操作者同参数重试为 200，修改同一重试标识的内容返回 409。仅用当前有权的精确 Prometheus 来源快照；前端尚未发送必要字段 | implemented |
| GET | `/traces` | `scope: app\|agent`, `view`, `q`（有界 TraceQL 选择器）, `service` | 应用范围 `TraceList`：同一完整来源集上的计数、分面、直方图、散点与表格；Agent 范围 503 | implemented |
| GET | `/traces/{id}` | | 应用范围 `TraceView`：已存储且闭合的 Span 树、关键路径说明与服务调用图；无可靠关联时省略日志/链接 | implemented |
| GET | `/services/discover` | 单个可选 `cluster`；重复参数返回 422 | 只读发现经授权的新鲜 Kubernetes 工作负载快照；返回稳定资源 ID、采集来源与时间、未知字段清单；同名歧义候选不返回 | implemented |
| POST | `/services` | `{ items: [{ id, namespace, resource_id?, tier, owner, slo, p99 }] }`；`resource_id` 为发现接口返回的稳定 CMDB 标识，提供时必须有 namespace | `{ created }` | implemented |
| GET | `/logs` | `q`, `levels`, `service?`, `provider?` | `LogSearch`；完整来源模式计数与趋势 | implemented |
| GET | `/changes` | `service_id`, `run_id`, `range?=24h\|7d`, `sources?=native` | 单一有证据的原生交付运行及所选窗口的 `ChangesView` | implemented |

韧性提案 POST 只接受当前真人会话、单一环境、精确服务与同服务开放调查，并在事务内复核 `services.view`、`proposals.change`、`canvas.view` 后保存不可变审阅正文和审计；六种 `kind` 均要求对应的有界 `desired` 字段，重复提交会建立新记录。正文中的目标源状态、风险、可逆性和执行路径仍未核实；此操作不创建执行动作、批准、外部变更或 GitLab 合并请求。`GET /services/{id}/resilience` 可由固定 HTTPS 聚合来源提供五类完整观测，服务端核对范围、修订、采集时间、项目数及完整 DTO；未配置或证据不全返回 `503 resilience_source_unavailable`，不能以空数组或零冒充真实状态。实际聚合器尚未部署或核验；详见[韧性视图与提案](v3-resilience.md)。冻结前端按钮只显示本地提示，整页仍待对接；本批只做源码接线和最小编译，未跑测试或真实来源联调。

`GET /changes` 仅投影已持久化且成功的原生 Argo Workflows 运行、经运行/制品/计划摘要核对的 GitOps 发布事件和精确来源的事故；`detail.diff/risk/alert` 来自有版本约束的人类审阅记录，不从流水线或时间相邻关系推断因果。`POST /changes/{run_id}/review` 辅助路由要求当前人类会话、`services.change` 及运行/发布/事故版本，写入不可修改的 `change.review.accepted` 审计；同一用户同参数重复提交不重复写入。GET 的 24 小时或 7 天告警带另要求服务端绑定的来源与运维确认的 `complete_from` 覆盖整个窗口，只读取该 `source_id` 的事故；缺少配置、历史不够、来源变更或审阅证据不全均返回 503。`complete_from` 是运维对持续投递和保留历史的声明，代码验证范围与当前绑定，不自动发现外部中断。冻结前端仍不发送 `service_id/run_id`，选中运行也不刷新详情；本批只按后端标准计入，真实来源与页面验收另做。接线和审阅请求详见[变更视图契约](v3-change-view.md)及[告警覆盖声明](../examples/v3-service-alert-coverage.md)。

服务目录和详情使用未归档应用/环境及服务配置、经审查的精确 Prometheus 看板、同环境持久发布与调查证据构造完整 DTO。告警数和据此提升的状态只计入服务端完整覆盖声明指定的 Alertmanager 来源；声明需匹配实际运维入口的精确来源/标签及有效应用环境，缺少声明、来源、权限、值班、SLO、发布或 AI 调查等必填证据时整体返回 503，不把未知填成零或健康。读取来源后复核原 Cookie 会话、应用/团队归属、授权、指标目标与覆盖绑定。完整覆盖是运维对上游路由的显式声明，代码不证明外部持续投递；接入与撤销步骤见[服务告警覆盖绑定](../examples/v3-service-alert-coverage.md)。详情的复合服务 ID 尚不能直接作为指标、日志、链路页的 `service` 参数，冻结页面跳转及真实外部来源联调另行验收。

资源发现现在返回 `resource_id`、Kubernetes `uid`、环境与集群；单集群条件在 2000 个候选扫描上限之前执行，整次请求共用 20 秒预算。选定集群之外的可信同名资源，以及同环境、同命名空间中陈旧、缺失或已关联的原始同名身份，均会阻止当前只按名称登记的候选进入结果；`ambiguous_workloads` 只给出数量，不泄露阻挡对象。`registered` 是授权环境内的登记数，不是所示集群的登记数。负责人和等级只作来源提示，`replicas` 可为空，`matched`/`missing` 尚无可信线索。注册时按当前环境、采集新鲜度、存在状态、UID、命名空间、是否已关联及 `resources.view` 重新核对。旧客户端只传名称时仅可匹配唯一工作负载；同名、过期或已关联身份返回 409，不会静默关联。当前前端未传 `cluster`/`resource_id`，并固定展示负责人、SLO、P99 与系统映射，发现到注册交互尚未接通。服务列表和系统映射超过 1000 条可见记录会返回 `422 result_limit`，不会返回不完整总数。拓扑仅显示五分钟内成功采集且环境吻合的现存节点，关联应用不会替资源借用其团队授权。

完整拓扑代码路径要求单环境、可见 `around` 中心、同应用的小邻域和已审查目录中的 `topology_v1` 精确 Prometheus 查询；节点健康/影响、关系流量和中心连接等六个对齐样本必须逐项匹配当前 CMDB 身份与修订。任一目录项会启用实例完整模式，未为某应用配置精确来源时明确 503；没有该目录项时仍返回标记 `complete_v3_dto: false` 的授权 CMDB 部分响应。布局只由有权图计算，`blast` 是观测受影响资源计数，不推断传播因果。运维必须核实 PromQL 和录制规则本身的应用/环境隔离；本集成尚无真实 Prometheus 或浏览器证据。详见[拓扑来源契约](v3-topology-view.md)。

观测查询只使用持久化应用、环境、负责人团队及已配置的外部目标绑定；不同来源绑定冲突返回 `409 ambiguous_binding`，范围过大返回 `422 scope_limit`。`GET /metrics/samples` 辅助接口可在单环境、唯一显式 V3 Prometheus 指标目标和操作者审查过精确范围的查询目录下返回有界真实标量样本；可见服务缺 `connectors.view` 在目标查询前返回 403，隐藏或不存在的服务同样返回资源不存在，读取中撤权不返回样本。完整 `GET /metrics/dashboard` 另要求经审查的 `dashboard` 查询目录一次返回 37 个时间点及请求、延迟、CPU、SLO、热力和排名等全部受信面板，再从持久发布、回滚和事件记录取得时间标记；缺项返回 503，不以样本补推。读取后复核精确 Cookie 会话、当前授权、目标绑定和 SLO 配置。图上的时间重合只表示并列证据，不证明发布引发事件。冻结指标页仍硬编码多处标签/版本/SLO/分析文案，且对齐开关未发送 `align`；真实 Prometheus 尚未联调，因此页面和产品未验收。`GET /logs` 在唯一授权目标及已配置的 Elasticsearch 或 Loki 范围内，分别查询所选等级的全窗总数、错误数、直方图、限量日志行和完整模板模式聚合；逐桶守恒、来源配置及当前授权/绑定均核对后才返回 `200 LogSearch`，包括精确零结果。缺少完整聚合返回 `503 log_patterns_unavailable` 和真实的部分证据；显式空 `levels` 返回空结果。模式来自受信采集模板或 Elasticsearch 类别聚合，不声称 AI 聚类；真实目标的历史映射/租户覆盖、脱敏和查询性能验收见 [日志来源接线](v3-logs-source.md)。

`POST /metrics/ask` 已有单环境、单持久应用及唯一授权 Prometheus 指标目标的有界快照与持久对话实现，正式入口也接入来源版本复核。快照只含实际样本，稀疏样本不证明整窗趋势；模型上下文将其视为未信任观察，不向该对话开放通用工具。首次接受和相同请求重试分别返回 201/200；来源或权限撤销后，对话读取、SSE 和后续模型工作拒绝继续使用该快照。冻结页面只提交 `view`/`question` 并跳普通对话，缺稳定应用、时间窗与重试标识；本批遵照用户要求未运行定向测试或真实 Prometheus/模型调用，`implemented` 仅指配置来源后后端具完整处理链。多实例上线前必须先让所有对话读取和模型工作实例识别 `metrics_source_required`，再开放创建流量；见 [指标提问来源与恢复](v3-metrics-ask.md)。

应用范围 Trace 由单环境持久 Tempo 目标绑定及当前 `traces.view` 确定；同一静态目标跨隐藏范围复用、选择器重叠或目标修订变化均拒绝。列表把最近一小时搜索触顶窗口继续拆分，最多 64 次查询、40 条 Trace、6 个目标，每条无时间界限拉取最多 500 个 Span 并验证唯一根、父链闭合与无环；超预算或来源失败返回 413/503，不用部分样本推总量。`200 TraceList` 的分面、十二个时间桶和散点来自同一返回全集。列表要求服务端 `tempo_search_fail_closed` 配置，设置前需核目标 Tempo 的 `fail_on_high_lag=true`；该配置默认关闭，不是运行时证明。详情的操作名称仅显示服务端白名单或路由模板，缺可靠日志/变更/Runbook 关联时省略链接。Agent 范围缺完整来源而返回 503。完整性仅指当次 Tempo 返回的已存储且闭合的 Trace，不证明采样遗漏或未插桩 Span；真实 Tempo、延迟配置及冻结页面 `q`/散点 `service` 接线尚未验收，见 [Trace 来源边界](v3-traces-source.md)。

## v4 新建流程（`create.ts`）

所有"新建"只修改平台自身配置；带"AI 起草"的接口只返回草稿，由人确认后再提交。

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/incidents/draft` | 必需 `alerts`：1–20 个当前受权、同应用环境的活跃告警 ID | 持久告警预填的部分草稿；未知影响、人员与作战群可空 | implemented |
| POST | `/incidents` | `Idempotency-Key` 头及 `{ title, severity, impact?, env, services: [id], alert_ids, commander?, war_room?, notify?, status_page?, open_canvas? }` | 持久人工事件、告警关联、审计及可选空白调查；外部动作标志请求返回 503，不发送或发布 | implemented |
| POST | `/alert-rules/generate` | 单环境、`application_id`、`{ text }`、`Idempotency-Key`；需规则修改权 | 经受审目录与有界模型生成的 `review_required` 草稿；不保存为生效规则 | implemented |
| POST | `/alert-rules/preview` | 单环境、`application_id`、`{ query, op, threshold, for_minutes, range }`；需规则查看权 | 真实 Prometheus 七天五分钟采样曲线与窗口；全量历史计数、路由和相似规则未知 | implemented |
| POST | `/alert-rules` | 单环境、`Idempotency-Key`、完整应用/环境/组/规则/标签定义与 `mode: draft\|dry_run\|enabled` | `draft` 在受权且受信来源/仓库均可用时导出不可部署 JSON 草稿，返回 `201 pending_review`；`dry_run`、`enabled` 尚无正式入口所需的隔离运行源、批准证明和独立执行链，返回不可用，不试运行或启用规则 | implemented |
| POST | `/alert-rules/import` | 单环境、`Idempotency-Key`、`application_id`、`mode: draft` 与至多 128 KiB 的 `yaml` | 有界解析、逐项核对当前来源；新规则导出不可部署草稿并返回 `201 pending_review`，全部重复返回 `200 duplicates_only`，不启用规则 | implemented |
| POST | `/notification-routes/test` | `{ labels }`：当前绑定应用与环境的精确样例告警 | 当前生效 Alertmanager 策略的节点路径、匿名接收方引用及分组时序；不发送通知或试算草稿 | implemented |
| POST | `/notification-routes` | `Idempotency-Key`、GET ETag 的 `If-Match`；绑定应用、匹配条件、匿名接收方引用及分组时序 | `202 pending_review`、本地草稿 ID 与经核对的 GitLab MR；当前 MR/运行态为 `null`，不激活路由 | implemented |
| POST | `/inhibitions/preview` | 单环境、`application_id`、`{ source, target, equal }`；目标仅明确的 P2–P4 | 可信完整七天 firing 历史上的潜在抑制条数与真实样例；实际命中未知；缺来源 501 | implemented |
| POST | `/inhibitions` | 单环境、`application_id`、`{ source, target, equal }`、`Idempotency-Key` 与 GET 强 ETag 的 `If-Match`；P1 不允许 | `202 pending_review`、本地草稿及经核对的 GitLab MR；不激活规则 | implemented |
| GET | `/runbooks/draft` | `source: template\|postmortem\|conversation\|import`, 必需 `from` | 有界、来源可核的确定性大纲及 `source_sha256`；不是 AI 生成或已核实操作步骤 | implemented |
| POST | `/runbooks` | `{ title, source, from?, expected_source_sha256?, steps, status: draft\|review }`；非空白来源必需引用，空白来源不得填写 | `{ id }`；来源变更 409，写动作步骤必需 `approval`，不发布或执行 | implemented |
| GET | `/postmortems/candidates` | | 有真实结案证据、尚无复盘且当前有权访问的事件；含实际证据数与持续时长 | implemented |
| GET | `/postmortems/draft` | 必需 `incident`：当前受权且有结案事件的旧事故 ID | 结案事实和时间线构成的部分草稿；根因、影响与行动留空待人填写 | implemented |
| POST | `/postmortems` | `{ incident_id, sections, actions, status: draft\|review }` | `{ id }`；同事务锁定已解决事件并写入复盘与审计，不触发外部动作 | implemented |
| POST | `/knowledge-sources/test` | `{ type, url, secret_ref?, application_id, team_id?, spaces? }`；`type` 限受支持的公开 Web Markdown、绑定 GitLab Markdown/Confluence/Notion/飞书 Docx 或本人上传文本 | 即时受限只读探测；不证明持续健康、同步或索引 | implemented |
| POST | `/knowledge-sources` | `{ name, type, url, secret_ref?, application_id, team_id?, spaces, schedule: "manual", chunk: "heading"\|"fixed", inherit_acl: true }`；类型与探测相同 | `{ id, status }`；登记不自动同步或索引 | implemented |

`POST /inhibitions/preview` 在当前单环境、环境级静默查看/管理权和有效应用绑定下，将精确环境与应用匹配器加入来源和目标两侧；目标 severity 仅允许明确的 P2–P4 集合。它要求受信只读来源证明完整七天 firing 区间历史，至多读取 500 条发生记录，返回潜在抑制目标数和真实告警名样例；`actual_inhibition_hits` 为 `null`。缺来源返回 501，来源失败或覆盖不完整返回 503，不用实时快照伪造七天命中。`POST /inhibitions` 再要求强 ETag 和幂等键，将同一约束的不可变草稿、审计与 GitLab 待审 MR 建立起来，返回 `202 pending_review`；不合并、不激活、不发送通知。两个 POST 都只按有条件的后端成功路径计 `implemented`，其来源配置和冻结页面仍须单独验收；见[抑制规则与草稿契约](v3-inhibition-drafts.md)。

`POST /alert-rules/generate` 仅在受审应用/环境查询目录、受保护模型配置及精确主机名单齐备时，对当前有权应用做一次无工具模型调用，严格解析模型输出及 PromQL 允许范围，持久化带审计的待人工审阅草稿；同键不重复调用，预算限流或结果不确定明确报错。模型返回后的结果写入事务再次锁定会话、应用、授权及尝试，失权不保存草稿。`POST /alert-rules/preview` 只向该目录显式绑定的应用隔离 Prometheus 端点读取七天五分钟采样；`sampled_count` 与窗口不是历史完整告警次数，真实总数、噪声、路由及相似规则来源未知时为 `null`。两条路由不创建或部署规则，未配置时明确不可用；部署仍须验证端点本身的应用隔离与累计模型费用限制。冻结新建页缺 `application_id` 和生成幂等键，且把未知采样字段当确定值，页面未验收；见[规则助手契约](v3-rule-assistant.md)。

规则写入三条主路由仅按 `draft` 的条件式后端成功路径计为 `implemented`。草稿导出需要已配置且与配置提升、通知路由及抑制草稿隔离的 GitLab 目标和固定 `aiops-alert-rule-drafts/` 分支前缀；外部写入期间保持当前会话、应用授权和草稿操作锁，结果不确定时保留同身份对账。导出的是待人工审阅的不可部署 JSON，不合并、不写入生效 Prometheus 或 GitOps YAML。正式入口尚未接入按应用隔离的 24 小时试运行 worker、提案 Passkey 批准证明、受信 YAML 部署计划/执行器和生效规则精确观察，故 `dry_run` 与 `enabled` 不可用；这两种模式及冻结新建页均未交付。详见[规则写入来源与边界](v3-alert-rule-writes.md)。

`GET /incidents/draft` 要求显式选择 1–20 条当前受权、同应用环境且未关联人工事件的活跃持久告警，按事实预填标题和最高严重度；影响、指挥官、参与者、作战群与 AI 分析未知。`POST /incidents` 要求人类会话、事件查看和修改权、单一服务与有效 `Idempotency-Key`；独立人工事件、告警关联、可选空白调查与审计在同一事务提交，重试同键同体返回既有结果，不发送外部通知。`war_room`、`notify`、`status_page` 请求在缺执行器时返回 503；`open_canvas` 另须画布查看与修改权。新增 `137_v3_incident_cases.sql` 未在现有库执行，案例相关的既有读取同样依赖它；人工事件目前无状态转移写入，而现有复盘只从已解决的 `ops_incidents` 选择，故不能宣称人工事件到复盘的端到端流程已闭合。冻结页面缺选中告警和幂等键，页面未验收。

`GET /runbooks/draft` 从已登记代码模板、已发布复盘、本人已完成对话或本人 Markdown 导入生成有界、来源可核的确定性大纲；模板在人工补充前没有操作步骤。来源文本作为不可信数据，响应 `source_sha256` 可供后续保存时比较，不能证明 AI 生成或恢复操作正确。`POST /runbooks` 在同一事务重核精确会话、单环境、当前知识管理权、来源状态与可选摘要；来源变化返回 409，初版、结构化步骤、来源快照、修订历史与审计原子提交，动作步骤必须要求人工批准，创建本身不发布或执行。未审核草稿只向维护者开放，review 向当前知识管理员开放；旧 Knowledge 和 Memory 读写入口同时以 `runbook://v3/` 来源前缀及持久 Runbook 关联阻断旁路，来源字段被旧路径改写亦不能绕过。冻结新建页缺 `from`、来源摘要及编辑步骤/动作请求绑定，页面未验。`GET /postmortems/draft` 在同一授权快照中读取有结案事件的旧事故及有界时间线，影响、根因、行动建议留给人填写，不是 AI 生成稿；响应携带精确结案事件序号，但后续 `POST /postmortems` 尚未绑定该序号，跨请求变更仍需复核。按本阶段后端判据计 `implemented`，冻结页面未验收。

公开 Web 来源只支持持久应用绑定、无凭据单页 HTTPS Markdown 的即时探测、登记及手动观察；V3 实例须显式设置 `AIOPS_V3_PUBLIC_WEB_READER_ENABLED=true`，未开启时相应读取返回 501。Git Markdown 只支持服务端配置的精确 GitLab HTTPS 仓库、数字项目、分支和 Markdown 路径前缀，凭据由 Vault 绑定读取；`/knowledge-sources/test` 先检查提交的应用/团队身份和当前授权再读取凭据，登记保存不含密钥的绑定指纹，手动同步将固定提交 SHA 的正文与证据一同持久保存，配置漂移、上游失败和读取后撤权均拒绝，详见 [Git Markdown 来源边界](v3-git-markdown-source.md)。Confluence 与 Notion 通过服务端精确环境/应用/团队、空间或页面、只读 API 与 Vault 绑定读取，详见[受限来源边界](v3-confluence-notion-sources.md)。飞书只读来源限服务端精确绑定的一篇 Docx，固定调用 `open.feishu.cn` 并比较两次文档修订，Wiki 目录/子页不在范围内，详见[飞书 Docx 边界](v3-feishu-source.md)。上传来源只接受当前本人、当前环境、服务器保存的原始 UTF-8 文本文件；登记和手动同步重核版本及授权，撤销原文件会删除其私有正文副本，详见[上传来源边界](v3-upload-source.md)。六类来源的探测只说明当次可读，登记不自动同步，手动读取证据不表示向量索引、持续连接或完整来源页。主 POST 两行按后端接口标准计入；主 GET 仍是部分 DTO，连接/同步/索引未知。冻结创建页真实模式目前只开放 Web/Git Markdown，Feishu 文案写 Wiki 且其余类型缺必需应用/团队请求与上传选择；真实 GitLab/Confluence/Notion/Feishu/Vault、浏览器和索引生产者尚未联调。

## Agent 链路（`traces.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/agent-runs/{id}/trace` | | 当前受权持久运行与已记录的净化 Span 元数据；无 Span 时可空，消息、工具参数/结果、评分与回合未知 | implemented |

Agent Trace 后端只能读受信生产者上报的有界 Span 元数据；模型消息、工具参数/结果、评分和完整回合仍无生产者，响应以 `null` 与 `missing` 标出。工具 Span 也不证明外部执行器的两阶段审计完成。本阶段按受权持久来源的后端口径计 `implemented`，完整追踪页面仍未验收，见[运行记录来源边界](v3-agent-runs.md)。

## 集成中心（`integrations.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/connectors` | `category`, `q`（名称、标识和供应商支持中文及拼音检索） | `ConnectorList`；计数基于当前可见全集，筛选只影响 `items`；`ok/error` 是最近 15 分钟的连接探测结果，不代表持续健康，过期、缺凭据或服务端凭据绑定缺失显示 `unverified`；登记工具偏好不生成 Agent 能力标签；未分页的可见记录超过 1000 条返回 413 | implemented |
| GET | `/connectors/{id}` | | `ConnectorDetail`：只返回当前有权的服务绑定；凭据文案区分未配置、服务端绑定缺失与已登记 Vault 引用，最近探测不代表持续健康；没有 Agent 执行器或查询绑定时 `tools`、`queries` 与能力标签为空，服务超过 1000 条返回 422 | implemented |
| POST | `/connectors/{id}/test` | | `{ ok, latency_ms }`；禁用返回 409，凭据用途未由服务端绑定返回 503，探测失败如实返回错误并审计 | implemented |
| PATCH | `/connectors/{id}` | `{ tools: [{ name, enabled }] }`；至少一项，`enabled` 必须是显式布尔值 | `{ ok: true, tools: [{ name, desc, enabled }] }`；在行锁下部分合并工具偏好并同事务审计；旧目录工具冲突返回 409，不静默删除；`enabled` 仅门控部分只读来源，不授予 Agent 执行能力；前端开关尚未发起此请求 | implemented |
| POST | `/connectors` | `{ type, url, auth, tools, webhook, credential_ref? }`；凭据仅传 Vault 引用；所有需要凭据的创建请求先保存为未启用登记，`webhook` 仅记录意图，不自动创建 Webhook | `{ id }`；未启用登记不可测试连接或启用工具，生成 ID 后由部署方配置精确凭据绑定，再通过辅助接口激活；无认证且不请求 Webhook 的非 GitLab 只读来源可直接启用 | implemented |
| GET | `/connector-types/{type}` | | `ConnectorType`（真实探测器支持的认证方式；暂无类型级权限范围与可执行 Agent 工具时返回空数组） | implemented |
| GET | `/mcp-servers` | | 当前授权的显式环境绑定、不可变发现摘要与人类审核偏好；未发现、未支持或没有归属生产者的数量/调用历史为 `null`；超过候选或可见绑定上限返回 413 | implemented |
| GET | `/mcp-servers/{id}` | 单一环境 | 当前绑定的完整领域详情、实际发现的工具/资源/提示词及审核状态；未知数组与调用历史为 `null`，远端元数据为未信任观察 | implemented |
| PATCH | `/mcp-servers/{id}/tools/{name}` | `snapshot_id`、`registration_version`、`registration_hash`、`tools_hash`、`tool_hash`、`expected_revision`、显式 `enabled` | 受当前登记、快照、工具与修订约束的审核偏好和审计；不授予 Agent 执行 | implemented |
| POST | `/mcp-servers/{id}/review` | 当前快照、登记版本/摘要、工具集合摘要及全部工具的哈希与决定 | 人类审核的原子结果和审计；批准工具默认关闭，无 Agent 执行器 | implemented |

MCP 主读取从当前授权的显式环境绑定、不可变发现快照与人类审核记录构造领域 DTO。完整发现只访问固定 MCP 端点，在一个有界 Streamable HTTP 会话中执行 `initialize` 以及已声明的 `tools/list`、`resources/list`、`prompts/list`；不执行工具、读取资源正文或获取提示内容。服务版本和数量只在成功观察后给出，未声明与未发现分开表示，未知值为 `null`。凭据只来自 `AIOPS_MCP_CREDENTIAL_ROOT` 下的私有文件，不宣称 Vault 已接入或当前凭据有效。最近发现成功不代表持续健康。旧固定参数绑定、应用所属关系和人类开关均不构成 Agent 执行授权；V3 还没有可归属的 MCP 调用记录生产者，因此调用次数及历史保持 `null`。服务端最多扫描 2000 条候选记录，最多返回 1000 条可见绑定，超限返回 413。领域 DTO 显式标出 `complete_v3_dto=false` 与冻结页面不兼容字段，表示页面尚未验收，而非后端缺失当前可验证来源；见 [MCP 领域链路](../examples/v3-mcp-domain.md)。

MCP 审核的三个辅助接口 `GET /mcp-servers/{id}/review-candidate`、`POST /mcp-servers/{id}/review-version` 和 `PATCH /mcp-servers/{id}/reviewed-tools/{name}` 提供同一受版本约束的机制，主写路由复用它。候选读取在新事务中重核当前账号、会话、应用/环境绑定、授权及最新发现，持锁至有界响应写完；写入要求当前有 `connectors.manage` 的人类身份、完整逐工具决定和匹配摘要/修订，审核与开关变化同事务审计。远端描述、输入 schema 和只读提示仍是未信任观察；批准的工具默认关闭，即使开启也没有 Agent 执行器。冻结前端的空审核请求及仅带 `enabled` 的开关请求会被拒绝；前端还不能渲染可空列表与调用历史，并以登记 ID 作跨环境行键，页面接入须另行对齐。

`GET /connector-types/{type}` 只描述已支持的连接器类型及探测器可接受的认证方式，要求当前环境的 `connectors.view`；未知类型返回按 `Accept-Language` 本地化的 404。版本、上游令牌权限与 Webhook 地址没有类型级可信来源，返回空值；尚无连接器支持的 Agent 工具执行器，因此 `tools: []`。本条后端路由完成不代表新建向导的静态项目映射、事件、深链及工具说明已接线，前端目录还有未支持的类型。
| GET | `/ingest/pull/{id}` | | `PullDraft`（请求模板、样例、字段映射、预览） | implemented |
| POST | `/ingest/pull/{id}/test` | `{}` 或 `{ mapping: { name_label, label_keys } }` | `{ ok, rows, dropped, captured_at }` | implemented |

Pull 仅使用服务端私有配置登记的 Prometheus `metric_resource` 目标及经审核的应用/环境受限查询；来源 ID 固定应用、环境、连接器、目标与查询摘要，变更身份须换新 ID。GET 实时运行单次有界 `query_range`，返回规范化样例和预览，不返回原始上游正文、PromQL 或凭据；POST 只接受空对象或经白名单约束的 `name_label` 与 `label_keys` 映射，计数来自本次真实查询。两路都要求当前应用/团队服务查看和连接器查看，测试另需连接器管理；单环境、当前会话、目标和授权在上游读取后再核。按来源与操作者的持久预算超限返回 429，上游警告、截断或不完整返回 502，审计与尝试只存净化摘要。配置为空时没有来源，未知 ID 返回 404；冻结页面仍取固定 Mock ID，编辑/测试控件未向真实接口提交。运维必须审核查询在 Prometheus 数据层的应用/环境范围；本机替身验证不能证明生产来源或 Vault 已连通。见 [Pull 来源边界](../examples/v3-pull-ingress.md)。
| GET | `/ingest/push` | | `PushView`（当前授权来源的真实计数与收据、签名协议说明、未部署配套栈）；无来源显示未配置 | implemented |
| POST | `/ingest/push/test` | `{ type }` | `{ code: 202, receipt_id }`；持久化 synthetic 事件，不计入真实事件数 | implemented |
| GET | `/service-mappings` | | `ServiceMapping` | implemented |
| POST | `/service-mappings/accept` | `{ candidates: [{ id, version, service_id, environment_id, system_id, binding_revision }] }` | `{ accepted, items }` | implemented |
| POST | `/service-mappings/discover` | | `{ suggestions, source_state, candidates }` | implemented |
| PUT | `/service-mappings` | `{ environment_id, items: [{ service_id, service_environment_id, binding_revision, bindings }] }`；1–50 个完整替换项 | `{ environment_id, items }`；返回持久身份、绑定及写后修订号 | implemented |
| GET | `/coverage` | 单个可选 `app`（精确应用 ID 或 key） | 授权应用/环境的配置证据矩阵；实时覆盖、评分与 Agent 影响未知值为 `null` 或明确可用性 | implemented |
| GET | `/webhooks` | | 当前授权通用 Push 与原生 Alertmanager/GitLab 入站登记；通用 Push 的 24 小时非合成收据、Alertmanager 的已入库接收次数，GitLab 历史次数未知；未接类别以 `availability: not_connected` 标记 | implemented |
| POST | `/webhooks/{id}/redeliver` | | 结果 | mock |
| POST | `/api-tokens` | `{ name, scopes, ttl }` | `{ id, secret_shown_once }` | mock |

`GET /coverage` 最多返回 200 条当前有权应用/环境行，候选按稳定游标扫描，容量只计授权行。连接器、资源及 Runbook 单元格再各自核对查看权限；登记目标或 CMDB 关联只证明配置，不证明持续数据覆盖。现有 Runbook 文档缺精确环境和文档可见性证据，因此该格保持 `unknown`；评分、类型百分比、评测失败归因与 Agent 影响没有可信生产者，数值为 `null`，可用性显式标注。当前冻结 `CoverageView` 类型要求非空数值且页面把未知格显示成绿色勾，整页尚不可按真实数据验收；本批未修改前端。后端本阶段只完成源码、契约和最小编译，未运行数据库、测试或浏览器验证。

服务映射候选目前仅来自管理员显式绑定的单个 GitLab 数字项目目标，不按标签、名称或部署记录推断。`POST /service-mappings/discover` 在一个环境中核对服务与连接器授权、近期探测和绑定后的激活审计，保存 15 分钟候选并返回 `source_state` 与可复核的候选身份，不直接改服务映射；超过 500 条可见未映射来源返回 422。`POST /service-mappings/accept` 只接受发现或 GET 映射格给出的候选 `id/version/service_id/environment_id/system_id/binding_revision`，在事务中复核候选唯一性、来源身份/修订、当前授权及绑定可用性，并将映射、候选状态与审计一同提交。旧的 `{ cells: ["service:system"] }` 只有展示字符串，无法约束来源和修订，已由候选请求体取代；冻结前端及 `types.ts` 尚未适配。V3 专用校验只允许与已登记来源完全相同的单个 GitLab 项目绑定，其余绑定继续交给原平台校验器；旧平台的全局 GitLab 拒绝规则没有扩大。迁移 `090_v3_service_mapping_candidates.sql` 保存候选；应用环境退役时候选会级联删除，管理审计另记接受决定。激活审计证明绑定时曾核验项目访问，近期通用探测不证明当前仍能访问项目。V3 实例拦截未迁移的旧环境编辑路由，旧模式也不能在已绑定 V3 的数据库上启动。隔离数据库 HTTP 测试覆盖候选、接受与审计，尚无正式进程对真实 GitLab 项目的端到端验收。

单环境 `GET /service-mappings` 在调用者有当前服务查看/修改及连接器查看/管理权时，为可见行附上包含稳定服务 ID、服务环境 ID、绑定修订和精确绑定的 `save` 快照；不安全的历史绑定仍可展示，但 `save.editable=false`，`all` 视图不提供保存快照。`PUT /service-mappings` 要求一个选定环境、1–50 个不同服务的完整绑定集合，缺失或 `null` 的绑定数组、展示格、重复/未知系统、跨环境对象及过期修订都拒绝；`bindings: []` 明确清空。服务/连接器授权、对象身份和精确 Cookie 会话在事务内复核，变化项的绑定、修订和审计一同提交，无变化项不新增审计。GitLab `code` 绑定仅可原样保留；新增/修改项目必须走候选发现与接受。其他绑定仍需运行时校验，示例见 `docs/examples/v3-service-mapping-save.md`。冻结前端目前发送空 PUT，且发现后不刷新、接受前乐观显示成功，文案误称来自标签/命名/部署；页面保存仍未验收。

Push 事件接入（供外部系统调用，不是前端调用）：`POST /api/v1/events/{alert|change|incident|resource|oncall}`。来源在私有配置中固定到一个应用、环境与允许类型；要求来源 ID、Bearer 令牌、`Idempotency-Key`、Unix `X-Timestamp` 和正文原始字节的 HMAC-SHA256 `X-Signature`。五条公开路由只豁免浏览器会话/环境门禁，由来源令牌与签名独立鉴权；未列出的相邻路由仍需浏览器会话。规范事件、24 小时幂等占用、净化收据与带精确环境的审计同事务持久化后返回 202；重复 409、无效规范体 422、鉴权失败 401、来源限流 429。已配置来源的 GET 只显示当前有权的应用/环境及其真实持久记录；管理测试生成 synthetic 记录，不证明外部发送端可用。配置、签名算法和恢复见[通用 Push 入站](../examples/v3-push-ingress.md)。此处只支持规范 `generic` 事件，不把供应商原生载荷当作已接入。冻结前端的测试请求尚未发送必需 `type`，复制的 curl 缺来源、时间戳和签名，配套栈按钮也未接部署控制器；页面未验收。公开部署前还需入口层无效令牌限流、数据留存及容量决策；本轮没有外部发送端、迁移现有业务库或页面浏览器实测。

`GET /webhooks` 在当前环境、连接器和应用/团队服务查看授权下展示已配置的通用 Push 来源，以及已有 Operations Alertmanager 和 Delivery GitLab 入站接收器中当前可见的登记。通用 Push 的过去 24 小时真实、非合成收据保留在 `deliveries`；Alertmanager 的次数/最近时间仅取同来源、同应用环境的已入库告警接收记录，不证明发送端持续健康。GitLab 历史收据未持久保存应用环境身份，重新绑定项目后无法安全归属，故原生 GitLab 的次数和最近时间保持 `unknown`。原生状态保持 `unverified`，不把通用 Push 收据冒充供应商投递。原生 GitLab POST 在 V3 中只对精确来源路由豁免浏览器会话/环境头；由已有来源令牌和项目绑定鉴权，并在同一事务内锁定当前未归档应用、应用环境、V3 环境绑定和本实例环境范围，才写入收据身份。这不开放相邻路由。未配置、没有可见来源及尚未接入的 API 令牌、对外 MCP、出站能力通过 `availability` 区分；V3 概览不读取凭据、不探测上游，也不启用出站发送。

通用 Push 请求最多纳入 50 个来源和每来源 5 类事件、返回 30 条收据；整个列表有 12 秒超时。原生来源按私有配置逐项核对当前持久应用环境与权限，来源过多可导致超时并返回 503；通用聚合查询尚无独立扫描行数上限，公开容量仍需评估。冻结前端不识别 `unverified` 和 `availability`，选中原生来源时仍显示汇总的通用 Push 收据，页面尚未验收。`POST /webhooks/{id}/redeliver` 按用户决定暂不启用并保持 `mock`：不读取签名凭据、不接出站 HTTPS 发送器、不向外部地址投递内部事件；`POST /api-tokens` 仍是独立 `mock`，入站收据 ID 不可用于出站重投。

连接器辅助激活接口（不计入 166 项页面契约）：`POST /connectors/{id}/activate`，单一环境下传 `{ credential_ref?: "vault://mount/path#field" }`。所有凭据读取须先匹配由部署方配置的层级、环境、连接器 ID、供应商、端点和引用；缺绑定返回 503 `credential_not_authorized`，不读取 Vault 或探测上游。GitLab 须先通过服务环境的显式目标绑定登记至少一个数字项目 ID；后端用 Vault 凭据对每个项目发起 GET 验证，再复核权限、连接器配置和绑定集合，成功后启用。其他已登记类型使用有界只读探测，成功后启用登记但工具仍关闭；无认证类型可传 `{}`，已保存 Vault 引用也可省略请求中的引用。通用探测只证明指定地址一次返回 2xx，健康端点可能不校验凭据，所以通用激活另返回 `credential_verified: false`；GitLab 的项目验证也不证明令牌只有只读权限。`webhook` 仅记录意图，`webhook_active` 始终为 `false`；探测失败返回 502 且不启用，GitLab 项目绑定变化须重新激活。`status: "ok"` 表示一次探测结果，不代表持续健康。配置和恢复见 [连接器凭据绑定](v3-connector-credentials.md)。

辅助 `GET /connectors/{id}/tool-settings` 在当前 `connectors.view` 与环境范围内读取登记工具开关，返回 `connector_enabled`、`registered_tools` 和固定 `agent_execution_available: false`，不暴露端点或凭据。它只证明持久偏好；主 `ConnectorDetail.tools` 仍为空，因为没有可执行的连接器 Agent 工具。主 PATCH 已有配置完成的连接器写入、响应和持久回读路径，按后端专用判据标为 `implemented`；前端目前不读取该辅助接口，也不调用 PATCH，页面和 Agent 工具执行均未验收。非 GitLab 草稿配置/激活仍是独立待闭合链路。

## Agent 运行台（`agents.ts`）

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/agents/lifecycle` | | 当前受权持久 Agent 与配置发布意图；评分、用量、阶段及待办未知 | implemented |
| GET | `/agents/{id}` | | 当前版本目录、修订及发布配置；实际流量、门禁展示和统计未知 | implemented |
| POST | `/agents/{id}/rollout` | `{ to, percent, expected_revision, override?, override_reason? }`；门禁放行需真人管理员与原因 | 保存并审计发布意图；`routing_connected=false`，不切流 | implemented |
| POST | `/agents/{id}/rollback` | `{ expected_revision }` | 保存并审计配置回退意图，未切实际流量 | implemented |
| PATCH | `/agents/{id}/level` | `{ level: L0..L3, expected_revision }`（受环境上限约束） | 持久权限等级配置和审计，执行侧未接 | implemented |
| GET | `/prompts` | | 当前授权、确有持久正文版本的 Prompt 目录；运行时使用和发布状态未知 | implemented |
| GET | `/prompts/{id}` | | 当前授权持久版本与原始正文；评测覆盖、使用和发布状态未知 | implemented |
| POST | `/prompts/{id}/trial` | `Idempotency-Key` 与 `{ case, a, b }`：同一 Prompt 的不同持久版本，案例需可信正文快照 | 两次受限无工具模型调用的真实文本与已记录来源；评分/好坏未知为 `null`，用量可空 | implemented |
| POST | `/prompts/{id}/versions` | `{ text }` | `{ version }`（`vN`）；正文、指针与审计同事务 | implemented |
| GET | `/agent-tools` | `kind: all\|read\|write` | 当前有权登记工具与已记录调用；未接执行器和覆盖证明明确未知 | implemented |
| GET | `/evals/{id}` | | 已保存评测元数据与受信结果汇总；数据集、逐例输出、雷达未知时可空 | implemented |
| POST | `/evals` | `{ agent_id, title, baseline_version, candidate_version }` | `{ id, status: "pending", execution_available: false }`，未启动评测 | implemented |
| GET | `/eval-cases/{id}` | | 持久登记及可选可信评测快照；缺快照时输出可空字段和 `missing` | implemented |
| POST | `/eval-cases/{id}/labels` | `{ next: correct\|incorrect\|partial\|unsure, note?, expected_revision }`；要求已有可信案例快照 | `{ next, id, revision }`；真人标签、快照绑定及审计同事务 | implemented |
| GET | `/agent-runs` | `filter: all\|failed\|blocked\|negative` | 当前受权持久运行列表、计数与 UTC 趋势；缺记录为可空未知 | implemented |
| GET | `/agent-runs/{id}` | | 持久运行/Span 元数据和投票；上下文、记忆、模型及工具内容未知 | implemented |
| POST | `/agent-runs/{id}/feedback` | `{ vote: up\|down }` | 当前本人对已完成运行的持久投票与计数 | implemented |
| GET | `/guardrails` | `filter: all\|blocked\|masked` | 当前受权持久策略及已上报护栏事件的七日统计；运行覆盖未知 | implemented |
| PATCH | `/guardrails/{id}` | `{ enabled }`（强制规则不可关闭）；目标须为受信目录已登记策略 | 持久修订及审计；`runtime_status=unconnected` | implemented |

Agent 治理接口保存目录、版本、Prompt 修订、待执行评测、发布意图、已上报运行/Span、反馈和护栏设置；尚无评测执行器、流量路由消费者、Prompt trial 的可信用例输入生产者、完整运行观测覆盖证明或真实调用护栏钩子的执行器。受权 `GET /agent-tools` 从持久工具目录及受信进程内运行 Span 读取已记录调用，缺记录显示未知；开关和风险级别不构成可执行 Agent 工具，响应明确 `execution_available=false` 和部分覆盖。`GET /eval-cases/{id}` 从持久登记及可选的不可变可信评测快照返回；无快照时输出真实登记、可空详情和 `missing`。标签 POST 只对已有可信快照、当前真人会话及匹配修订号写入，快照有受限进程内生产 API，但评测执行器尚未接线；默认新案例因此返回 409，冻结页面请求/返回也不兼容。

Prompt 列表只返回有真实版本的已授权记录，支持最多 160 字符的 `q`，超过 200 条可见项返回 413；详情在同一读取事务中核对 Agent、归属、授权和正文，真实正文与 `vN` 可回读，发布状态与运行时使用关系保持未知。辅助 `POST /prompts` 可在当前有权 Agent 下登记首版，主 `POST /prompts/{id}/versions` 保存下一版非空白原文，身份、版本指针与审计同事务。冻结前端默认 ID 在新库不存在且“新建”未调用登记接口，页面未验收。`POST /prompts/{id}/trial` 要求当前会话、单环境、同 Prompt 两个持久版本、幂等键，以及受信进程内登记的有版本案例输入；经显式受保护配置与精确主机名单启用后，最多两次无工具模型调用。调用前和每次结果入库前复核当前权限，失权只保存不含生成文本的计费/不确定元数据。两个结果均持久且最终再授权通过才返回真实文本；评分和好坏无生产者，保持 `null`，用量缺提供者证据也可空。默认模型未配置返回 503，当前启动流程无可信案例输入生产者，普通案例返回 409；同键不重复付费，新的键可能再次计费。跨键累计费用与并发配额尚未接入，启用真实付费模型前须配置部署层配额和限流；真实调用与页面未验收，详见[Prompt 试运行](v3-prompt-trial.md)。`POST /evals` 仅保存 `pending` 元数据，核对两条不同的已登记版本、当前应用/环境与授权，不排队或执行评测；冻结前端重跑发送空请求体却提示“已排队”。创建事务锁定账号与授权来源，但未锁当前 Cookie 会话，并发注销与在途创建的顺序尚未专测。生命周期五路由按受权持久配置、修订冲突和审计计入后端 `implemented`，发布/回滚不改变真实流量；首次稳定版没有既有基线可评测，只能由符合条件的真人管理员填写原因走放行门禁。运行四路由按真实持久运行/Span 与反馈计入，计数仅代表受权已保存记录，缺字段显式可空；详见[生命周期来源](v3-agent-lifecycle.md)及[运行记录来源](v3-agent-runs.md)。现有前端对评分、发布 token/门禁、评测详情、Trace 和运行统计的 DTO 与请求体仍不兼容，也未提交 `expected_revision` 与放行原因，页面未验收。

`GET /evals/{id}` 只读已保存的部分评测记录：同一事务锁定评测、Agent、有效应用/环境、当前会话和授权，并以创建审计核对原始归属；缺审计或 Agent 改属的历史记录隐藏。`metrics` 仅在有可信结果记录时返回已保存值，待执行状态不合成分数；`radar`、用例输出和数据集仍无生产者，以 `null` 和 `missing` 标记。主 GET 按本阶段真实来源与未知值标准计 `implemented`，完整 `EvalExperiment` 页面未验收。已新增按应用/目标/行为定位创建审计的索引，迁移尚未在现有库执行；规模未实测。`GET /eval-cases/{id}` 保留原始应用/环境列和可信快照，新增标签修订号及截断标志；POST 在案例行锁下复核真人、来源、当前权限、修订和快照，并将标签、快照 ID 和审计原子保存。

`GET /guardrails` 从当前有权的持久策略及已上报、具备决策时间和修订证明的事件计算七个 UTC 日桶与策略计数；`aggregation_basis=reported_events`、`coverage=unknown`、`partial=true`，零条上报不证明零次实际触发，原始内容与结论保持 `null`。`PATCH /guardrails/{id}` 对已由受信目录登记的策略在单环境 `agents.manage` 下持久修订、阻止关闭强制规则并审计，响应 `runtime_status=unconnected`；当前主程序还没有注册策略的启动来源，干净环境无策略时返回 404。两条路由只按后端持久读取/配置写入能力计 `implemented`，不表示 Agent 执行器消费策略或页面验收。`CompleteRun` 已在取得运行行锁后使用数据库实际时钟写完成时间，仍需在接入执行器时验证顺序与并发，详见[护栏领域边界](v3-guardrails-domain.md)。

## 设置（`settings.ts`）

`GET /members` 在当前账号、所选环境与 `members.view` 权限下从持久成员/角色表读取可见成员，并在读取事务中复核当前授权。`last_login`、`counts.no_mfa`、`mfa_grace_days` 缺可信来源时为 `null`，`items[].mfa="na"` 表示未知，不代表未启用；`filter=no_mfa` 返回 `503 member_mfa_unavailable`，不以空集合冒充筛选。组层级和目录同步状态也未知，`complete_v3_dto=false`。主 GET 仅按本阶段受权真实读取与可空字段标准计 `implemented`，冻结前端仍将未知当确定数值显示，页面未验收。新增辅助成员详情/联系方式 PATCH 只改本地持久姓名和电话，不触及角色、MFA 或外部目录身份；同步与 MFA 提醒已接默认关闭的受信来源配置：只有私有配置精确绑定当前实例、单环境及经审定的飞书租户时才存在条件式成功路径；未配置或选择其他环境明确不可用。MFA 证据请求与响应均需回证实例、租户和环境，且完整覆盖当前可见真人及已核实 open ID；提醒须有专用飞书 bot，成功仅证明 API 接受并落库，批次中途失败时已送达者不能回滚。不因本地 TOTP 或目录缺席推断 MFA/离职。两条主 POST 只按条件式后端成功路径计 `implemented`；同租户权限、真实凭据、迁移、发送及冻结页面未验收，见[成员来源与提醒配置](v3-member-sources.md)。

`POST /members/invitations` 只由当前真人会话在单环境、全局 `members.manage` 与 `environment.view` 下发出本地只读 `viewer` 邀请。发送前与接受时复核该可编辑角色的实际权限，禁用环境准入、应用/团队范围及任何写权限；邀请码有 256 位随机熵，只存哈希，24 小时且 SMTP relay 接受 DATA 后才可用于公开的 `POST /auth/invitations/accept`。接受事务创建本地账号与审计，但不自动授予环境或应用访问。发送只在完整配置的证书验证 TLS 465 SMTP、受保护密码文件、与实例域一致的 HTTPS 产品入口和安全 Cookie 下启用；未配置返回 503，半配置或错域在启动时失败。Relay 接受不证明最终邮箱送达；匿名接受在进程内限速，部署多副本仍需入口共享限速。迁移 `138_v3_member_invitations.sql` 尚未在现有库执行；当前产品没有接受页，真实邮件与浏览器未验。本阶段只做代码、契约和最小编译，未跑测试或发送邮件。

可选的本人 TOTP 登记、确认、验证与恢复码，以及七条本人 WebAuthn 登记、账户断言和删除辅助接口见 [V3 后端辅助接口](v3-backend-aux.md)。TOTP 仅配置持久服务器密钥后注册；passkey 仅在受信实例 RP 域名、精确 HTTPS Origin 和安全 cookie 配置满足条件且显式开启时注册。账户断言不提供提案审批保证；这些接口也未补齐成员目录、`/me` 或 `/security` 的完整前端契约。

| 方法 | 路径 | 参数 / 请求体 | 响应 | 状态 |
| --- | --- | --- | --- | --- |
| GET | `/me/profile` | 单一所选环境；需当前真人账号、活动会话与环境查看权 | 受信身份来源完整覆盖联系人验证、MFA 因子和全部活动会话设备/登录方式时返回持久资料 `ProfileView`；缺证据 503 | implemented |
| PUT | `/me/profile` | 单一所选环境、完整 `ProfileView`；仅姓名、显示名、职务、时区和通知可编辑 | 同事务复核当前证据、会话及权限后保存可编辑字段与审计；联系人、部门和安全字段不可借完整提交改写 | implemented |
| DELETE | `/me/sessions/{id}` | | 204 | implemented |
| GET | `/members` | `filter: all\|admin\|no_mfa\|disabled`, `q`；`no_mfa` 缺证据时 503 | 当前受权持久成员与角色；登录、MFA、组与目录来源未知时可空 | implemented |
| POST | `/members/invitations` | `{ email, role?: "viewer" }` | `{ ok: true }`；仅 SMTP relay 接受且状态落库后成功 | implemented |
| POST | `/members/sync` | 单环境、空请求体；当前人类成员管理员 | 受审、完整的单环境飞书目录快照落库后返回 `{ added, updated }`；新成员默认禁用 viewer，不自动授应用/环境权限，身份冲突失败 | implemented |
| POST | `/members/mfa-reminders` | 单环境、空请求体；当前人类成员管理员 | 同实例、租户与环境绑定的完整新鲜 MFA/飞书收件人证据下，受限向未登记成员发送；飞书接受并保存回执后返回 `{ sent }`，部分发送或不确定结果明确报错 | implemented |
| GET | `/roles` | | `{ items: Role[] }` | implemented |
| GET | `/roles/{id}` | | `RoleDetail` 与响应头 `X-Role-Matrix-Revision` | implemented |
| PUT | `/roles/{id}` | `{ matrix }` 和 GET 返回的 `If-Role-Matrix-Revision`；机器身份的“批准”列必须拒绝 | `{ ok: true }` 与新矩阵版本响应头 | implemented |
| GET | `/security` | | `SecurityView` | mock |
| PUT | `/security` | `SecurityView` | `SecurityView` | mock |
| GET | `/me/notification-settings` | | `{ quiet: { enabled, from, to }, digest: off\|daily\|weekly, subscriptions }`；只读取当前账号已保存偏好，无记录时返回平台默认值，不代表通知渠道已接通 | implemented |
| PUT | `/me/notification-settings` | 部分更新 `{ quiet?: boolean\|{enabled,from,to}, digest?, subscriptions?: { [existing_id]: boolean }\|现有订阅完整列表 }` | 保存并返回当前账号合并后的完整通知偏好；订阅标识与文案不能在此接口新建或修改，不代表通知已投递 | implemented |
| GET | `/me/ai-preferences` | | `{ default: ModelChoice, canvas_inherit, show_reasoning, auto_context, memory, budget_warn_tokens }`；`GET /models` 的 `default` 取自这里 | implemented |
| PUT | `/me/ai-preferences` | 同上 | 同上 | implemented |
| GET | `/workspace` | | `{ name, slug, timezone, locale, week_start, retention_days, data_region, brand }` 和强 ETag；当前有效账号在同一读取事务内复核所选环境的 `workspace.view` 后返回持久化配置。`data_region` 是保存的部署声明，不证明数据实际驻留 | implemented |
| PUT | `/workspace` | 同上八字段完整对象（`brand` 含 `name,logo,pending`），`If-Match` 必须为 GET 返回的强 ETag；仅平台管理员且具全局 `workspace.manage` | 保存并审计后返回持久化对象及新 ETag；无前置条件 428、旧版 412 | implemented |
| GET | `/audit` | `filter: all\|user\|agent\|system`, `q` | `AuditView` | implemented |
| GET | `/audit/{id}` | | `AuditDetail`（含哈希链校验结果） | implemented |
| POST | `/audit/export` | 过滤条件 | `{ url }` | implemented |

`GET/PUT /me/profile` 已接可选受信 HTTPS 身份证据来源：只有当前真人账号、活动 Cookie 会话、单环境查看权、完整且新鲜的联系人验证、MFA 因子及全部会话设备/登录方式证据与本实例持久登记一致时才返回完整 `ProfileView`。PUT 只改可编辑资料和通知偏好，事务内复核并审计；不允许借完整提交改写联系人、部门或安全事实。两条仅按有条件后端路径计 `implemented`；默认未配置来源时返回 `503 identity_evidence_unavailable`，仓库尚无可提供全部设备/登录方式的权威生产者，真实来源与冻结页面未验收；见[本人资料证据边界](v3-profile-evidence.md)。会话删除是独立接口。

`POST /members/sync` 与 `POST /members/mfa-reminders` 可通过受保护的单环境成员来源配置接入飞书目录、权威 MFA/收件人证据及专用提醒机器人，默认不配置时明确 503。同步在读取完整目录后于写入事务重核管理员、会话和成员归属；新成员默认禁用且不授环境/应用权限，也不因快照缺席自动离职。提醒要求同实例、租户和环境绑定的五分钟内完整证据，单次最多 20 人、每位收件人 24 小时及每位管理员每小时最多 100 条；外发前再次锁定管理员、会话、目标和待发送台账，发送与回执审计保持同一限时授权事务。部分或未知发送结果不能当作全部失败并自动重试。两条只按受审来源齐备时的后端条件成功路径计 `implemented`；仓库未提供实际 MFA 权威服务，飞书租户/权限、现有库迁移、真实发送与冻结页面均未验收；见[成员来源配置](v3-member-sources.md)。

`GET /workspace` 在读取事务内复核当前有效账号与所选环境 `workspace.view`，读取持久化记录；缺失或损坏时返回 `503 workspace_unavailable`，不会合成默认值。`PUT /workspace` 严格校验单个 JSON 对象、已知字段和值，使用该记录与旧设置接口共用的 `revision` 比较 `If-Match`，在事务中复核账号、平台管理员身份和全局 `workspace.manage`，保存与审计一起提交；`data_region` 和品牌字段为只读部署声明，提交值必须与已存值相同。新建本地或目录导入成员现在继承已保存的 `timezone`、`locale`；已有成员不被改写。slug、`week_start` 和留存天数仍只是保存配置，尚未驱动链接、日历或清理行为；`data_region` 不能证明物理驻留。冻结前端工作区页尚未传递 GET 的 ETag，真实 PUT 会返回 428，整页未验收。

角色/成员列表与详情只展示所选且已授权环境内的对象与成员；只有同时具备全局模块查看和全局 `environment.view` 的管理员可盘点无环境归属的旧记录。`GET /roles/{id}` 在同一授权事务快照中读取角色、成员和授权；平面五列矩阵无法无损表达的授权返回 `409 matrix_editor_required`，不将部分授权伪装成可编辑单元格。响应头 `X-Role-Matrix-Revision` 只版本化授权矩阵，不版本化描述或成员。`PUT /roles/{id}` 要求该版本的精确十进制 `If-Role-Matrix-Revision`，缺失 428、旧版 409；所选单一环境只用于鉴权，提交的是跨所有环境的全局授权，因此要求全局 `roles.manage` 和全局 `environment.view`。读写事务均在角色授权锁之后复核并锁定发起请求的精确 Cookie 会话与有效账号；写入还在角色行锁下复核权限和版本，拒绝机器身份审批，保护最后一位有效人类管理员，授权、版本与审计共同提交。PUT 仅返回 `{ ok: true }` 与新版本头，不在提交后重读并泄露角色详情。冻结页面不读取或发送版本头，真实保存会 428，默认选择的 `approver` 也可能与持久角色 ID 不符；后端两条路由已闭合，角色整页尚未验收。其他设置主契约仍按各行状态判断。

## 后端辅助接口（不计入上述 166 项）

- `GET /investigations/{id}/nodes/{node}/messages`：只读当前持久化调查分支的 `{ branch_id, revision, messages }` 与最近的运行/失败尝试。服务端以分支记录核对调查、应用、环境和所属团队，再检查当前 `canvas.view`；消息保留历史 JSON 原貌，旧记录可能没有 `created_at`，不补造时间或 AI 摘要。当前上下文超过 24 KiB、损坏或缺失返回 503；不存在、路径不匹配或无对象访问权返回 404。
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
| 2026-09-29 | WAN-119 角色详情 GET/PUT 按后端单路由判据标为 `implemented`；矩阵写入新增 `If-Role-Matrix-Revision` 强前置条件，版本随 GET/PUT 响应头返回，用于防止旧页面覆盖持久授权。冻结页面尚未传递该头，页面验收另列。 |
| 2026-09-29 | 后端单路由证据使 `GET/POST /canvases`、`GET /config-sync`、告警规则 dry-run、服务映射 discover/accept 标为 `implemented`；页面仍待适配和实连。服务映射接受体由展示字符串 `{ cells }` 改为稳定候选身份、版本和绑定修订 `{ candidates }`，以防过期/错绑；本批按后端范围先更新本契约与 OpenAPI，冻结 `types.ts`、Mock 和页面尚未同步，页面继续使用 Mock 模式。 |
| 2026-09-28 | 冻结 v1。拆分合并行；补 `GET /conversations/{id}/events` 行；画布节点对话请求加 `model`、`reasoning`，响应加 `new_branch`；新增实例与环境一节和 `X-AIOps-Environment`。 |

## 后端实现提示

- 后端实现一个端点后，把状态改成 `implemented`，并删除或保留对应的 Mock（保留时仅用于开发）。
- 页面接线时须将服务映射候选请求/响应同步到 `types.ts`、Mock 与调用页面，再用前端 `npx tsc -b` 核对受影响页面；当前后端优先采用可复核候选身份，原因与临时分歧见变更记录。
- 推理强度（v4）：后端目前支持 OpenAI 兼容 Chat Completions（含 Ollama）与 Claude Messages，尚无 Responses API 与强度透传。`GET /models` 的档位由后端按模型声明返回；映射为 `reasoning.effort`（Responses）、`reasoning_effort`（Chat Completions）、Claude 的 thinking 参数或 Ollama 的 `think` 开关。
- 示意数据只在 Mock 模式下出现，页面顶部有"示意数据"标记；真实模式下缺失的能力应返回空列表或明确的"待接入"状态，不能用示意数据代替。
