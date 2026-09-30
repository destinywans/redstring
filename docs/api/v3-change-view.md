# V3 变更视图：`GET /changes`（WAN-120）

## 范围与请求

读取一个已注册服务在**单个选中环境**中的一条原生 Argo Workflows 交付运行。请求为 `GET /changes?service_id=…&run_id=…&range=24h&sources=native`。`service_id` 是应用 ID、NUL、环境 ID 串接后的 base64url；`run_id` 必须是该服务和环境中已持久化的原生 `delivery_runs.id`。`range` 可选 `24h`（默认）或 `7d`，`sources` 可选且仅支持 `native`（默认）。缺少、重复或非法参数返回 422；运行不属于已选范围时返回 404，不暴露其存在。

读取要求当前会话及应用/环境 `services.view`、`events.view`。服务、运行、事件、环境和授权在汇总前后复核。撤权、来源改变或无法复核时不得返回旧投影。本接口只读，不启动工作流或改变发布。

## 200 的来源与字段

完整 200 符合 `web/src/v3/api/types.ts` 的 `ChangesView`。`pipelines` 仅有被选中的一条运行，`detail.id` 对应同一 `run_id`。`stages/jobs` 来自该运行的持久化节点状态；不可把未运行的节点显示成 `ok`。`lanes/axis` 和 `sources` 限于本服务、本环境、本时间窗内已持久化且可见的事实。`sources.count` 是来源记录数，不是健康度；只有实际参与投影且完成来源核对的来源才可 `on=true`。

`kpis` 只使用可复算的 `runs`、`succeeded`、`failed`、`deployments` 计数和对应序列。运行以 `delivery_runs` 的稳定身份、状态和时间为准；部署以经核验的 GitOps 事件及运行、环境和制品摘要关联为准，不能仅凭 Argo CD 历史、Kubernetes 健康或工作流成功推断发布。窗口内的告警带仅取服务端核验的精确 `source_id` 的 `ops_incidents` 时间段。覆盖声明须包含所选应用、环境和不晚于完整窗口起点的 `complete_from`；没有整窗声明时，零告警不能表示无告警，也不能返回完整视图。声明的运维确认与撤销要求见[服务告警覆盖绑定](../examples/v3-service-alert-coverage.md)；代码不会从空事故表推断外部持续投递。不会生成变更失败率、提交至生产前置时间、MTTR 或告警因果结论，因为此切片缺少这些指标所需的归因和起止事件链。

冻结 DTO 中 `detail.diff`、`detail.risk`、`detail.alert` 均为必填非空对象。它们只能来自注入的已审阅 `ReviewSource`。`AuditReviewSource` 只读取不可修改的审计链中明确标为 `change.review.accepted`、`display_approved=true` 的最新人工审阅版本，并核对它指向前一个版本；按**精确运行 ID、运行快照摘要和更新时间、制品与提交摘要、发布计划、服务、GitOps 执行及真实事故身份和更新时间**交叉核对。审阅文字应标明观察或判断语义，不能将时间相邻自动解释为因果。`risk` 不从流水线状态、告警严重度或模型自由生成；`alert.incident` 必须指向可见的真实事故。主读取路由和审阅写入路由已在 WAN-117 主程序注册。缺审阅证据、摘要不符、GitOps 部署证据不全、事故覆盖未知、来源或授权读取中发生变化时返回 503，不以空字符串、示例文案或 mock 填补。

## 人工审阅写入

`POST /changes/{run_id}/review`（部署路径 `/api/v1/changes/{run_id}/review`）发布主视图必需的审阅证据。请求必须选定单个环境，并携带以下 JSON 字段：

- `service_id`、`decision: "accept_for_display"`、`expected_run_digest`、`expected_run_updated_at`；
- `execution_id`、`expected_plan_digest`、`expected_artifact_digest`、`expected_commit_sha`；
- `incident_id`、`expected_incident_updated_at`、`expected_diff_digest`；
- `diff: {path, minus, plus}`、`risk: {title, meta, text}`、`alert_text`。
- 再次审阅时提供 `expected_previous_review_id`，值为当前最新审阅的 `review_id`；首次审阅省略或传空字符串。

两个 `expected_*_updated_at` 均为 RFC 3339 时间。`expected_diff_digest` 是小写 64 位 SHA-256 十六进制摘要：对 `diff` 按 `path`、`minus`、`plus` 字段顺序序列化为紧凑 UTF-8 JSON 对象（Go `encoding/json` 的结构体编码，无额外空白），再计算 SHA-256。路径中的 `run_id`、`service_id`、当前选中环境和所有预期版本共同确定本次审阅的对象。服务端核对当前会话和非机器用户身份，以及该环境的 `services.view`、`events.view`、`services.change` 授权；核对已持久化且成功的原生运行、GitOps 执行与部署事件、真实事故及其版本。请求不能创建运行、发布或事故，也不改变外部系统。

`diff` 文本是**人工陈述**，由 `expected_commit_sha` 和自身的 `expected_diff_digest` 约束版本；这不是系统从代码源取得并证明过的差异。`risk` 和 `alert_text` 也是人工判断。关联真实事故只证明身份与时间匹配，不证明部署导致事故。审阅者须用文本表达其依据与不确定性，不得把相邻事件写成已证实因果。

通过核对后，服务端写入一条不可修改的 `change.review.accepted` 审计记录，供 GET 的 `AuditReviewSource` 精确读取。同一用户、同一对象和相同请求重复提交返回 200 且不重复写入；首次发布及有前序 ID 约束的再次审阅返回 201。运行或事故更新使旧审阅失效时，审阅者需使用当前来源版本与最新审阅 ID 再次审阅；旧记录保留在审计链中。不同审阅与当前已接受记录冲突、前序 ID 过期或来源版本过期时返回 409；无权返回 403，跨范围或不可见对象返回 404；非法请求返回 422；必要存储不可用返回 503。写入后的 GET 仍要求独立的告警来源覆盖证明，审阅记录本身不能代替覆盖。

## 状态与前端差距

- 200：全部必填字段具有上述可核来源，且最终权限/来源复核成功。
- 422：缺少或非法 `service_id`、`run_id`、`range`、`sources`，含重复值。
- 403/404：依当前权限和资源可见性处理，无权的运行不得泄露详情。
- 413：时间窗内的运行、部署事件或事故超过有界读取上限（分别 500、500、200），不返回截断后的误导性指标。
- 503：已选范围内缺少或改变必需的运行、部署、告警覆盖、审阅证据，或读取来源失败；响应不包含部分 `ChangesView`。

当前前端 `GET /changes` 不传 `service_id` 和 `run_id`，且点击流水线后不按选中项重新取 `detail`。因此本切片的主接口在现有前端上尚无成功路径；前端集成须先补全请求参数和选中运行刷新，再进行页面验收。辅助 `GET /changes/records` 的 GitLab CI 元数据不提供上述部署与审阅证据，不能代替主接口。
