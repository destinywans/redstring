# V3 后端辅助接口

这些接口用于检查或记录已接入的真实来源，**不替代** `v3-contract.md` 中的页面接口，也不计入其 166 项完成数。它们使用同源会话、`X-AIOps-Environment` 和 V3 角色矩阵；响应只含能核实的字段。页面目前按完整 `AlertRule` / `KnowledgeSource` 读取数据，不能直接接这些部分响应。

| 方法 | 路径 | 参数与含义 |
| --- | --- | --- |
| GET | `/api/v1/investigations/{id}/nodes/{node}/messages` | 只读当前持久化分支的 `{ branch_id, revision, messages }`，以持久化调查/应用/环境/所属团队核对当前 `canvas.view`。返回原始历史消息 JSON，旧记录的可选字段（包括 `created_at`）可能缺失，不合成图节点、时间、摘要或模型输出。上下文最大 24 KiB；对象不存在、路径不匹配或无访问权返回 404，当前上下文缺失、过大或损坏返回 503。完整调查图、节点写入和祖先回写仍未接通。 |
| GET | `/api/v1/incidents/{id}/timeline/manual` | 按当前事件应用/环境及 `events.view` 读取已持久化的人工备注，按保存时间/ID 排序；返回 `{ items, scope: "manual_only" }`，展示姓名取当前成员记录。未知或无权事件 404，超过 500 条或读取故障 503，不返回部分结果。不合成系统/AI 时间线、影响或错误预算，也不使完整事件详情可用。 |
| POST | `/api/v1/mcp-servers/{id}/discover` | 单环境、显式绑定及当前 `connectors.manage` 下发起有界 MCP `tools/list`，无请求体。保存不可变的未信任工具声明快照、版本/摘要和同事务管理审计；响应仅返回快照身份、摘要、数量、时间以及 `approved=false`、`health_verified=false`，不含原始声明、端点或凭据。请求后再核对授权、登记版本/内容和绑定实例。上游失败或声明无效时记录脱敏失败审计并返回 502；发现不授予审核、启用或执行权。 |
| GET | `/api/v1/changes/records` | 必填 `service_id`、`binding_id`，可选 `page`（1–1000）；从已显式绑定的同环境 GitLab 项目读取每页 20 条流水线记录。调用前后均复核服务和连接器权限，缺少来源返回未接入，上游错误返回 502。只证明流水线自身状态与时间，不推断部署、风险或告警关联。 |
| GET | `/api/v1/changes/merge-requests` | 必填 `service_id`、`binding_id`；可选 `page`（1–1000）、`state`（`all`/`opened`/`closed`/`locked`/`merged`）、`target_branch`。在同一明确绑定的 GitLab 项目按页读取 20 条合并请求身份、分支、状态、提交标识和上游时间；须启用 `list_merge_requests`。请求和上游响应均有界，读取前后复核来源和授权；非法筛选 422，变更 409，来源不可用 503，上游失败或损坏 502。无差异、部署或风险判定。 |
| GET | `/api/v1/changes/commits/{sha}` | 必填 `service_id`、`binding_id` 和完整 40/64 位十六进制 SHA；从同一绑定项目读取标题、作者/提交时间及父 SHA，须单独启用 `get_commit`，不调用差异接口。读取前后复核来源和授权；目标无效 422，缺失 404，变更 409，来源不可用 503，上游失败或损坏 502。无正文、差异、邮箱、部署或风险判定。 |
| GET | `/api/v1/alert-rules/observed` | 读取受权环境及应用内、与受信规则组绑定的 Prometheus 告警规则摘要。来源未配置/全部不可用时返回 503；部分环境失败返回 206，并列出 `unavailable_environments`。 |
| GET | `/api/v1/alert-rules/observed/{id}` | 读取同一来源中的规则表达式、状态、标签和实际评估时间。没有可信的历史预览、阈值、通知路径或 AI 初查设置。 |
| GET | `/api/v1/knowledge-sources/observed-documents` | 仅读取有明确环境归属、已发布且受权的文档身份。本接口不提供来源登记或同步状态，`partial` 为 true，未接入字段为 null；不返回正文。 |
| GET | `/api/v1/knowledge-sources/registrations` | 读取当前环境与授权范围内的来源登记配置，不返回 Vault 引用。连接、同步、文档数和索引状态仍未知；返回 `complete_v3_dto: false`。 |
| POST | `/api/v1/knowledge-sources/registrations` | 在单一环境下登记来源及 Vault 引用，需要 `knowledge.manage`，审计与登记同事务。只返回 `unverified`，不启动同步。 |
| POST | `/api/v1/knowledge-sources/test` | 当前仅校验参数和管理权限，缺少 Vault 来源连接器时返回 `501 source_connection_test_unavailable`，不会返回模拟成功。此路径在 166 项主契约内，状态仍为 `mock`。 |
| GET | `/api/v1/uploads/{id}/content` | 下载本人上传的文件，或内联读取经重新验证格式的图片。可不传环境头；后端根据上传记录核对实例环境、当前 `conversation.view` 授权和所有者。普通文件以附件下载，响应禁缓存、禁 MIME 猜测；损坏图片拒绝返回。该辅助读取不改变 `POST /uploads` 的主契约状态。 |

变更证据三接口的 `service_id` 是 `base64url(application_id + NUL + environment_id)`；`binding_id` 必须指向该服务在所选环境中持久化的数字 GitLab 项目绑定。请求需要当前 `services.view` 和 `connectors.view`，连接器及相应只读工具均须启用。返回值仅是 GitLab 报告的源记录，不使主 `/changes` 页面变成已接通。

告警规则源由 `AIOPS_V3_ALERT_RULES_CONFIG` 指向私有 JSON，字段示例见 [v3-alert-rules.json](../examples/v3-alert-rules.json)。每个环境只可绑定一个 Prometheus 只读端点；还需把响应中的精确规则组 `file` + `name` 绑定到一个已登记的应用。标签中的 `environment` 和 `application` 只是交叉校验，不能单独决定归属。配置文件中的令牌文件路径为绝对路径，令牌值不写入配置、日志或仓库。未配置时辅助规则接口明确返回未接入，主页面接口仍保持 `mock`。

来源登记由迁移 `045_v3_knowledge_source_registry.sql` 单独保存，保留现有文档与索引。知识来源主接口仍需实际连接、同步结果和索引状态；不能把单篇文档或仅登记的配置伪装成已连接来源。没有前端契约适配之前，不将辅助响应接到现有知识来源页。迁移尚未在 PostgreSQL 执行。

MCP 发现快照由迁移 `070_v3_mcp_discovery_snapshots.sql` 保存，并以新绑定实例 ID 区分解绑后重绑。迁移在 `store.Migrate` 阶段执行；既有业务库尚未执行。快照不可更新或删除，当前没有历史保留期，后续审核与运维设计需要定义容量与留存策略。真实 MCP 服务尚未连接验证。
