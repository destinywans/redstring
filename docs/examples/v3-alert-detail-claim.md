# V3 告警详情与认领：来源及接线边界

`GET /api/v1/alerts/{id}` 读取 `ops_incidents` 的持久事件身份、状态、来源 ID、应用环境、负责人和摘要，并读取同一事件的 `ops_notifications` 台账。通知的 `created_at` 是本平台入队时间，`status=sent` 只表示通知传输接口接受，不能证明接收人已阅读。最多返回最新 500 条通知；更多历史以 `notifications_truncated=true` 和 `unavailable` 中的 `notifications_complete` 标明，不阻止人工认领。关联通知的应用或环境与事件不一致时整体返回 503。`revision` 是事件行的 PostgreSQL `xmin`，仅用于认领时比较事件状态和负责人；它不覆盖可能独立变化的通知记录，因此不是整个详情响应的 HTTP ETag。

当前 Alertmanager webhook 入库没有保存原始标签、当前 Alertmanager 适配器没有可与持久事件精确关联的 fingerprint/startsAt 组合，Prometheus 规则及范围查询也没有经确认的“此事件对应此序列和阈值”绑定。因此详情将 `source`（供应商名）、`rule`、`series`、`labels`、`triage`、`runbook` 和分组/抑制/变更关联返回 `null`，并列入 `unavailable`。`source_id` 是非空的持久入站身份，不证明此刻对应配置仍启用，也不是推测的供应商名；`grouping.incident.id` 是持久事件自身 ID。没有对应通知记录时，`notifications: []` 是台账的真实空结果。未知状态或严重级别返回 503，不能填成 P4、健康、阈值零或虚构曲线。

认领请求示例：

```http
POST /api/v1/alerts/{id}/claim
Content-Type: application/json
X-AIOps-Environment: prod

{"expected_revision":"12345"}
```

请求必须由当前有效的**人类**会话发起。事件行、当前应用及环境映射、负责人团队、会话和 V3 授权来源在同一事务内核对；同时要求 `events.change` 与 `events.view`，避免仅有变更权限的人从认领响应读取详情。认领人、审计与更新后的详情在同一事务中形成。版本过期返回 412；同一人的当前版本重试不重复写入；其他人已认领或事件不再可认领返回 409。平台认领不会自动停止 Alertmanager 或外部值班升级。

WAN-117 集成已将同一 V3 策略的团队授权回调注入 `v3events.Service.AuthorizeTeamTx`，使当前 `owner_team_id` 范围的 `events.view/change` 与应用范围策略一致，并生成总 OpenAPI、更新主契约和路由就绪记录。回调未配置时团队专属授权仍失败关闭。

冻结前端目前发送空认领请求，且把 `series`、`labels`、`source` 和关联字段视为非空；认领后提示“值班升级已停止”。这与后端版本请求和真实来源不符，页面 HTTP 验收须等前端按 nullable 结果渲染、发送 `expected_revision` 并移除错误提示。`implemented` 仅指本阶段配置来源时的后端代码路径，不能据此宣称冻结 `AlertDetail` 页面已验收。若将来接入指标、阈值和 Alertmanager 当前状态，应新增精确的服务端事件来源绑定，验证同一持久事件、应用、环境和现行授权，并在上游读取后再核权限；仅凭标题、时间接近或客户端标签不能建立关联。
