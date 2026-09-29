# V3 本地上传知识来源

WAN-123 将已有的本地上传文件作为一个应用、一个环境内的知识来源。先通过 `POST /api/v1/uploads` 上传 `kind=file` 的 UTF-8 文本文件，再使用响应给出的 `/api/v1/uploads/{id}/content` 作为来源 `url`。来源只接受服务器保存的原始文件；粘贴、图片、外部链接、任意 URL 和其他用户或环境中的文件不能登记。`secret_ref` 为空，不读取 Vault。

可用后缀为 `.txt`、`.log`、`.md`、`.csv`、`.json`、`.yaml`、`.yml`，保存的 MIME 和字节必须与文件类型一致；作为知识来源的正文上限为 128 KiB。普通会话附件的上传上限与这里的来源上限不同。文件按原样作为不可信纯文本保存，不执行其中的内容，也不从文本中的链接继续抓取。

| 操作 | 范围与结果 |
| --- | --- |
| `POST /api/v1/knowledge-sources/test` | 提交 `type=upload`、精确 `url`、空 `secret_ref`、`application_id` 和可选 `team_id`，`spaces` 为空。要求当前环境与应用/团队的知识管理权限，并核对原文件的所有者、环境、类型和正文。成功只说明探测时可读；不登记或持久化健康状态。 |
| `POST /api/v1/knowledge-sources` | 提交相同来源身份及名称，`schedule=manual`、`inherit_acl=true`、`chunk=heading` 或 `fixed`。在事务中核对会话、授权、应用/团队、原文件和版本，然后登记绑定；不自动同步或索引。辅助 `POST /api/v1/knowledge-sources/registrations` 使用相同的原文件边界。 |
| `POST /api/v1/knowledge-sources/registrations/{id}/sync` | 人工触发受限读取；在保存观察、正文、哈希、版本和审计的事务中重新锁定原文件并比较。文件被撤销或发生漂移时不提交新的正文证据。 |
| `GET /api/v1/knowledge-sources` | 仅在当前范围、最新读取、原文件及私有正文/观察证据仍一致时报告文档数与读取时间。登记可继续出现，但缺少完整证据时这些字段为 `null`。连接健康、定时同步、向量分块和索引状态仍未知。 |
| `POST /api/v1/uploads/{id}/revoke` | 文件所有者在同一显式环境中、具有当前 `conversation:change` 权限时撤销原文件。事务删除文件与其私有来源正文副本，并记录不含正文的撤销记录和审计；返回 `id` 与 `revoked=true`。旧会话引用和来源登记不会变成可读取文件。 |

来源版本覆盖文件 ID、所有者、环境、名称、MIME、大小和正文 SHA-256。登记时保存初始版本，后续读取不能悄然改绑另一个文件。撤销后，已保存的读取历史仍是历史观察记录，但不再满足当前文档证据，页面不能将其显示为可用正文或已索引。此接口不是文件删除的批量管理功能，也不会自动撤销任何其他文件。

迁移 `135_v3_upload_source_revoke.sql` 保存撤销记录；`136_v3_feishu_upload_sources.sql` 将原文件与私有来源正文关联，并在撤销时级联删除正文。受控上传和隔离数据库验证不能证明生产上传、跨服务副本或部署状态；这些仍需在目标环境单独验证。
