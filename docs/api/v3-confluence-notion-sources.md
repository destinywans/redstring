# V3 Confluence 与 Notion 知识来源

这两个来源使用服务端固定的只读范围。请求中的 URL、Vault 引用、环境、应用和可选团队必须与一条配置完全一致；客户端不能借登记请求选择另一个空间、页面、API 地址或凭据。留空 `AIOPS_V3_EXTERNAL_SOURCE_BINDINGS` 时不安装对应 Reader，探测和同步明确返回未接入。

## 配置和授权

`AIOPS_V3_EXTERNAL_SOURCE_BINDINGS` 是严格校验的 JSON 数组。每条包含 `type`（`confluence` 或 `notion`）、`source_url`、`endpoint`、`credential_ref`、`tier`、`environment_id`、`application_id`、可选 `team_id` 和 `connector_id`。Confluence 还需数字 `space_id`，绑定一个站点的一个空间；Notion 还需标准 UUID `page_id`，绑定一个页面。`endpoint` 是精确的只读 API 基址。配置示例见 [`.env.example`](../../.env.example)。实例仅安装与当前 tier 相同的绑定。

Vault 引用必须是 `vault://mount/path#field`；令牌保留在 Vault 中。运行进程另需私有的 `AIOPS_VAULT_URL` 和 `AIOPS_VAULT_TOKEN`。凭据读取同时匹配 tier、环境、连接器 ID、来源 provider、API 基址和引用。Confluence 需预先取得可读取该站点与空间的 Bearer 访问令牌；Notion 需预先创建具有 Read content 能力并获分享目标页面的连接。当前配置不执行 OAuth 授权或自动续期。

| 操作 | 范围与证据 |
| --- | --- |
| `POST /api/v1/knowledge-sources/test` | 提交精确 `type`、`url`、`secret_ref`、`application_id`、可选 `team_id`；Confluence 再提交单个 `spaces` ID。检查当前管理授权、应用映射及绑定，先审计尝试，进行有界只读探测，结束时重核授权。结果只是当时可读取，不保存持续健康状态。 |
| `POST /api/v1/knowledge-sources` | 使用相同来源范围，`schedule=manual`、`inherit_acl=true`、`chunk=heading` 或 `fixed`；事务内检查会话、应用/团队、绑定并登记，不自动同步。Confluence `spaces` 必须恰好为配置的一个数字 ID；Notion 必须为空。辅助登记路由使用相同绑定边界。 |
| `POST /api/v1/knowledge-sources/registrations/{id}/sync` | 人工读取已绑定范围，在外部请求前后检查配置指纹、应用/团队及授权；读取和正文、观察、来源版本、审计一同提交。任何不完整、超限、远端失败、漂移或撤权使本次事务不提交。 |
| `GET /api/v1/knowledge-sources` | 只对当前仍获应用/团队/环境授权、且最新成功读取的绑定快照、文档与观察哈希一致的来源显示文档数和读取时间。连接健康、定时同步、向量分块与索引状态仍未知；零值不代替未知。 |

Confluence Cloud 使用 REST API v2 的空间页面分页读取，并把 storage 正文转换为安全纯文本；不支持且无法完整转换的宏拒绝同步。分页游标只能继续访问配置的 API 和空间；页面 ID、空间、状态、版本及正文需逐页核对。Notion 先读页面元数据，再以 `include_transcript=true` 读取同一页面的 Markdown；子页面只作为链接，不纳入该登记范围。Notion 返回截断、未知块或带签名凭据的媒体 URL 时，不把结果标为完整。保存普通 HTTP 链接前会去掉查询参数和片段，并在正文标注这种脱敏；依赖参数的 Dashboard 链接可能需要从原始来源重新打开。正文和非秘密绑定快照保存在独立的 V3 来源表中，不进入旧知识文档授权路径。两种来源都有请求时间、响应大小、页数、文档数和总字节预算；超限不保存部分结果。

来源在两次手动读取之间可能变更或撤权，因此最近成功读取只证明历史证据，不证明此刻仍能连接远端。实际 Confluence OAuth 租户与 Notion 工作区未在本批受控测试中连通；需要分别验证令牌权限、共享范围和运行环境网络。

已观察页面若从下次读取中消失，系统不能只凭该结果区分“页面删除”和“来源权限收窄”，因此拒绝本次更新并保留上次已提交的快照，待人员核实来源范围。上游版本未变化而标题或正文变化、以及版本回退，也会拒绝更新。重新登记可显式接受变更后的来源范围。

官方接口依据：[Confluence REST v2 页面](https://developer.atlassian.com/cloud/confluence/rest/v2/api-group-page/)、[Confluence 分页](https://developer.atlassian.com/cloud/confluence/rest/v2/)、[Notion 页面](https://developers.notion.com/reference/retrieve-a-page)、[Notion Markdown](https://developers.notion.com/reference/retrieve-page-markdown)。
