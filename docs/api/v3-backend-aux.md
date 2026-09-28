# V3 后端辅助接口

这些接口用于检查或记录已接入的真实来源，**不替代** `v3-contract.md` 中的页面接口，也不计入其 166 项完成数。它们使用同源会话、`X-AIOps-Environment` 和 V3 角色矩阵；响应只含能核实的字段。页面目前按完整 `AlertRule` / `KnowledgeSource` 读取数据，不能直接接这些部分响应。

| 方法 | 路径 | 参数与含义 |
| --- | --- | --- |
| GET | `/api/v1/investigations/{id}/nodes/{node}/messages` | 只读当前持久化分支的 `{ branch_id, revision, messages }`，以持久化调查/应用/环境/所属团队核对当前 `canvas.view`。返回原始历史消息 JSON，旧记录的可选字段（包括 `created_at`）可能缺失，不合成图节点、时间、摘要或模型输出。上下文最大 24 KiB；对象不存在、路径不匹配或无访问权返回 404，当前上下文缺失、过大或损坏返回 503。完整调查图、节点写入和祖先回写仍未接通。 |
| GET | `/api/v1/incidents/{id}/timeline/manual` | 按当前事件应用/环境及 `events.view` 读取已持久化的人工备注，按保存时间/ID 排序；返回 `{ items, scope: "manual_only" }`，展示姓名取当前成员记录。未知或无权事件 404，超过 500 条或读取故障 503，不返回部分结果。不合成系统/AI 时间线、影响或错误预算，也不使完整事件详情可用。 |
| GET | `/api/v1/connectors/{id}/tool-settings` | 按当前授权环境和 `connectors.view` 读取持久登记的工具开关及本地化目录名称，返回 `{ connector_id, environment_id, connector_enabled, registered_tools, agent_execution_available: false }`。不返回端点、Vault 引用或 Agent 工具能力；未知/越界 ID 返回 404，已存工具与当前目录冲突返回 503，不静默丢弃设置。此辅助读取不使主页面的 PATCH 开关交互完成。 |
| POST | `/api/v1/mcp-servers/{id}/discover` | 单环境、显式绑定及当前 `connectors.manage` 下发起有界 MCP `tools/list`，无请求体。保存不可变的未信任工具声明快照、版本/摘要和同事务管理审计；响应仅返回快照身份、摘要、数量、时间以及 `approved=false`、`health_verified=false`，不含原始声明、端点或凭据。请求后再核对授权、登记版本/内容和绑定实例。上游失败或声明无效时记录脱敏失败审计并返回 502；发现不授予审核、启用或执行权。 |
| GET | `/api/v1/changes/records` | 必填 `service_id`、`binding_id`，可选 `page`（1–1000）；从已显式绑定的同环境 GitLab 项目读取每页 20 条流水线记录。调用前后均复核服务和连接器权限，缺少来源返回未接入，上游错误返回 502。只证明流水线自身状态与时间，不推断部署、风险或告警关联。 |
| GET | `/api/v1/changes/merge-requests` | 必填 `service_id`、`binding_id`；可选 `page`（1–1000）、`state`（`all`/`opened`/`closed`/`locked`/`merged`）、`target_branch`。在同一明确绑定的 GitLab 项目按页读取 20 条合并请求身份、分支、状态、提交标识和上游时间；须启用 `list_merge_requests`。请求和上游响应均有界，读取前后复核来源和授权；非法筛选 422，变更 409，来源不可用 503，上游失败或损坏 502。无差异、部署或风险判定。 |
| GET | `/api/v1/changes/commits/{sha}` | 必填 `service_id`、`binding_id` 和完整 40/64 位十六进制 SHA；从同一绑定项目读取标题、作者/提交时间及父 SHA，须单独启用 `get_commit`，不调用差异接口。读取前后复核来源和授权；目标无效 422，缺失 404，变更 409，来源不可用 503，上游失败或损坏 502。无正文、差异、邮箱、部署或风险判定。 |
| GET | `/api/v1/alert-rules/observed` | 读取受权环境及应用内、与受信规则组绑定的 Prometheus 告警规则摘要。来源未配置/全部不可用时返回 503；部分环境失败返回 206，并列出 `unavailable_environments`。 |
| GET | `/api/v1/alert-rules/observed/{id}` | 读取同一来源中当前授权且稳定 ID 唯一的规则表达式、状态、标签和实际评估时间；可见重复 ID 返回 `503 alert_rule_identity_conflict`，不会任选一条。未知 ID 且范围内来源失败返回 `503 rule_source_partial`，完整来源无匹配返回 404。没有可信的历史预览、阈值、通知路径或 AI 初查设置。 |
| GET | `/api/v1/knowledge-sources/observed-documents` | 仅读取有明确环境归属、已发布且当前受权的文档身份，并在输出前按文档 ID 复核当前标题、有效应用/环境映射及团队归属；历史归属不明、异常元数据和带 `source_registration_id` 的未验证来源文档不显示。来源登记另有接口；本响应不联接登记、同步或索引归属，`reason: "document_only_projection"`、`partial: true`，未接入字段为 null；不返回正文。 |
| GET | `/api/v1/knowledge-sources/registrations` | 读取当前环境与授权范围内的来源登记配置，不返回 Vault 引用。连接、同步、文档数和索引状态仍未知；返回 `complete_v3_dto: false`。 |
| POST | `/api/v1/knowledge-sources/registrations` | 在单一环境下登记来源，需要 `knowledge.manage`，审计与登记同事务。仅无凭据、单页、HTTPS Markdown Web 来源可省略 `secret_ref`，且要求空 `spaces`、手动计划与继承来源访问；其他类型仍需 Vault 引用。只返回 `unverified`，不启动同步。 |
| POST | `/api/v1/knowledge-sources/registrations/{id}/sync` | 单环境、`knowledge.manage` 下通过明确配置的只读 Reader 观察已登记来源；须完整遍历、逐项验证 ACL 与归属，至多 500 项，成功只返回读取 ID、观察数、`index_state: "not_proven"` 和 `complete_v3_dto: false`，不导入正文。当前生产构造未注入 Reader，因此返回 `501 source_reader_unavailable`。 |
| GET | `/api/v1/knowledge-sources/registrations/{id}/evidence` | 只读当前受权来源最近一次观察及可精确归属到该观察的 ready 文档/分块数量。无观察时 `last_read`、`index` 为 null；即使有归属记录，`index.complete` 仍为 false，不推断完整索引。 |
| POST | `/api/v1/knowledge-sources/test` | 来源类型有明确配置的只读 Prober 时，成功仅表示当时服务端可读取，不持久化同步或证明对所有用户公开、应用/团队范围及索引。公开 HTTPS 单页 Markdown 可省略 `secret_ref`；其他类型仍需 Vault 引用。当前生产构造未注入 Reader，返回 `501 source_connection_test_unavailable`，不会返回模拟成功。此路径在 166 项主契约内，状态仍为 `mock`。 |
| POST | `/api/v1/runbooks/{id}/reviews` | 独立管理者按已提交版本选择 `approve` 或 `request_changes`，后者必须填写说明；拒绝自审和非法状态。审核、版本历史与审计持久化；批准后才在事务外尝试索引，响应单独报告语义检索就绪和是否需重试。 |
| POST | `/api/v1/runbooks/{id}/reindex` | 单环境、对象级 `knowledge.manage` 下重试已发布 Runbook 的语义索引；请求审计先落库，索引在事务外执行。响应分别给出 `index_status`、`semantic_search_ready` 与 `index_retry_required`；Embedding 失败不撤销人工发布，关键词检索仍可用。 |
| GET | `/api/v1/runbooks/{id}/record` | 读取持久化的结构化步骤及版本、维护者、应用/环境、来源类型；返回 `partial: true`，明确缺少用量、事件处置和执行核验。旧文档无结构化步骤时 503，不把全文猜成步骤。 |
| GET | `/api/v1/runbooks/{id}/history` | 读取版本、审核与只读验证预览，各集合最多 100 条；审核说明只对维护者/管理者显示。`execution_verification_available: false`。超限返回 413，不截断冒充完整。 |
| POST | `/api/v1/postmortems/{id}/publish` | 复盘创建者以外的当前 `knowledge.manage` 成员在 `review` 状态发布；要求原事件仍已解决且结案事件仍存在，发布与审计同事务。完整复盘详情与 AI 生成仍未接通。 |
| GET | `/api/v1/memory/facts` | 仅列出当前受权、来源文档仍已发布且版本/正文归属匹配的事实，返回 `partial: true` 与缺失置信度、召回、过期及层级/策略字段；超过 500 个候选事实返回 413。此接口不替代完整 `MemoryView`。 |
| GET | `/api/v1/uploads/{id}/content` | 下载本人上传的文件，或内联读取经重新验证格式的图片。可不传环境头；后端根据上传记录核对实例环境、当前 `conversation.view` 授权和所有者。普通文件以附件下载，响应禁缓存、禁 MIME 猜测；损坏图片拒绝返回。该辅助读取不改变 `POST /uploads` 的主契约状态。 |

变更证据三接口的 `service_id` 是 `base64url(application_id + NUL + environment_id)`；`binding_id` 必须指向该服务在所选环境中持久化的数字 GitLab 项目绑定。请求需要当前 `services.view` 和 `connectors.view`，连接器及相应只读工具均须启用。返回值仅是 GitLab 报告的源记录，不使主 `/changes` 页面变成已接通。

告警规则源由 `AIOPS_V3_ALERT_RULES_CONFIG` 指向私有 JSON，字段示例见 [v3-alert-rules.json](../examples/v3-alert-rules.json)。每个环境只可绑定一个 Prometheus 只读端点；还需把响应中的精确规则组 `file` + `name` 绑定到一个已登记的应用。标签中的 `environment` 和 `application` 只是交叉校验，不能单独决定归属。配置文件中的令牌文件路径为绝对路径，令牌值不写入配置、日志或仓库。未配置时辅助规则接口明确返回未接入，主页面接口仍保持 `mock`。

来源登记由迁移 `045_v3_knowledge_source_registry.sql` 单独保存，迁移 `119_v3_public_web_source.sql` 只放宽公开 Web 的空凭据规则，保留现有文档与索引。公开 Web 抓取只记录受限观察身份和正文哈希，不把正文发布到共享索引；现有共享检索尚未按团队范围过滤。知识来源主接口仍需实际连接、同步结果和索引状态；不能把单篇文档或仅登记的配置伪装成已连接来源。没有前端契约适配之前，不将辅助响应接到现有知识来源页。新增迁移只在一次性隔离 PostgreSQL 中验证，尚未在现有业务库执行。

Runbook/复盘治理新增迁移 `110`；来源观察证据为 `111`–`112`；来源锚定的记忆事实/冲突为 `113`–`114`。它们仅在一次性隔离 PostgreSQL 中执行过验证，尚未在现有业务库迁移。`GET /runbooks/draft`、`GET /postmortems/draft`、`GET /memory` 以及记忆状态/冲突操作虽有路由，均不满足当前整页 DTO 与交互契约；状态见 [V3 页面契约](v3-contract.md)。

Agent 目录、评测记录、运行上报与护栏策略分别使用迁移 `115`–`118`。护栏事件可附带受信生产者在策略决策时获取的数据库时间与策略修订，晚到记录按该历史区间核对；当前没有实际执行器接入或完整护栏覆盖证据。Agent 的 20 条主路由均为部分治理/上报能力，不计作整页 `implemented`，具体缺失字段和固定不可用响应见 [V3 页面契约](v3-contract.md) 与机器 OpenAPI。

MCP 发现快照由迁移 `070_v3_mcp_discovery_snapshots.sql` 保存，并以新绑定实例 ID 区分解绑后重绑。迁移在 `store.Migrate` 阶段执行；既有业务库尚未执行。快照不可更新或删除，当前没有历史保留期，后续审核与运维设计需要定义容量与留存策略。真实 MCP 服务尚未连接验证。
