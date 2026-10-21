# 手工事件状态与复盘来源（WAN-117/118/123）

手工 `INC-*` 事件由 `v3_incident_cases` 保存，与 Alertmanager 告警记录 `ops_incidents` 分开。创建时 `open_canvas=false` 是合法输入，因此解决事件与复盘不能依赖一定存在的调查画布或 `investigation.closed`。旧事故的真实调查结案来源继续保留；不把它回填成手工事件状态事件。

## 手工事件状态

辅助 `POST /api/v1/incidents/{id}/status` 仅接受真实手工事件 ID，严格请求体为：

```json
{"status":"resolved","expected_revision":1,"request_id":"case-closure-001","reason":"Recovery verified by the operator"}
```

`reason` 可省略，存在时去空白后为 1–2000 字符；`request_id` 为 8–128 字符的受限 ASCII 标识。详情 GET 返回独立 `status_revision`，供提交 `expected_revision`。只允许 `active → mitigated`、`active → resolved`、`mitigated → resolved`；已解决状态不可再写。当前真人会话、选定环境、应用/负责人团队和 `events.view`、`events.change` 都要在事务中成立。写入只记录事故治理状态，不改告警或基础设施。

新转换以 201 返回持久事件及当前状态，精确同一操作者、`request_id` 与请求内容的重试以 200 返回原事件。原事件是历史记录，响应的 `current_status` 和 `current_revision` 才是锁定时的当前状态。换体重用 ID 为 409，旧修订为 412，无效流转为 409；不可见对象为 404。操作者、数据库时间、前后状态、修订、请求身份与管理审计一同持久保存。只有真正写入的 resolved 状态事件可成为新的复盘解决来源；旧库中没有该事件的 resolved case 保持来源未知。

## 两类复盘来源

复盘候选、证据草稿、创建、记录、看板汇总和独立审核发布共用同一来源边界：

| 来源 | 可用条件 | 预览与保存绑定 |
| --- | --- | --- |
| 旧 `ops_incidents` | 原事故已解决、同一调查已关闭、真实 `investigation.closed` 事件与报告仍存在 | `source_revision=ops:<close_event_seq>`；旧请求的 `source_close_event_seq` 继续受支持 |
| 手工 `v3_incident_cases` | 当前状态为 resolved、`resolved_status_event_id` 指向本事件的真实 resolved 转换且修订一致；可以没有调查画布 | `source_revision=case:<status_event_id>`；创建时必须提交该精确来源 |

候选与草稿只输出可证明的证据。无 Canvas 手工事件的调查报告、调查时间线、事故时长、指标、影响和根因不补造；相应字段为 `null` 或列入 `unknown_fields` / `missing`。手工事件创建到解决的治理时间差不能直接当作实际故障持续时长。草稿只给人填写复盘的起点，不调用 AI。

创建锁定当前会话、授权、应用/环境/团队归属与精确解决来源；来源修订或生命周期变化返回 409 并要求重新预览，来源参数格式错误返回 422。章节、行动项、绑定来源和审计在同一事务保存，不执行行动。发布另需不同于创建者、持久身份为 `human` 的当前知识管理员；再核保存的旧调查结案来源或手工 resolved 状态事件及当前修订，变化时拒绝发布，不向外部系统发消息。同一位审核人也可以写明原因把待审核的复盘退回草稿（`POST /api/v1/postmortems/{id}/return`，PA-128），原因记在审计里，作者改完后重新提交。

## 数据与验收边界

迁移 `151_v3_incident_case_lifecycle.sql` 增加手工事件状态修订、只追加转换事件及真实解决事件引用；迁移 `152_v3_postmortem_case_sources.sql` 使复盘能保存旧调查结案或手工状态事件二选一的精确来源，并允许手工事件没有调查 ID。旧复盘行保留原结案外键和 `source_kind=ops`。两项迁移尚未在现有业务库执行。

后端代码、聚合 OpenAPI 与静态构建可供 Review；未运行数据库迁移、HTTP、测试、真实来源或页面。冻结前端尚不提交状态版本/请求 ID，复盘创建也不提交手工事件 `source_revision`，不能据此声称页面已验收。主契约仍按后端代码成功路径计数，辅助状态路由不增加 166 项主路由数量。
