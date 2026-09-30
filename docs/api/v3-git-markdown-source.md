# V3 Git Markdown 知识来源

本来源族读取**已配置的 GitLab HTTPS 仓库**。运维人员在服务器配置中固定仓库 URL、GitLab API 地址、数字项目 ID、分支、Markdown 路径前缀、环境、应用及可选负责人团队。请求只能选择与配置完全相同的 URL、Vault 引用和范围；不能选择项目、分支或路径。登记时保存配置指纹及不含凭据的绑定快照；后续配置变更不会悄然改变已登记来源，需要重新登记。当前不支持通用 Git HTTPS 克隆、定时或推送触发同步。

## 启动配置

`AIOPS_V3_GIT_MARKDOWN_BINDINGS` 是严格校验的 JSON 数组；留空表示不安装 Git Reader。一条占位示例见 [`.env.example`](../../.env.example)。每条必须包含 `source_url`、`endpoint`、`project_id`、`branch`、`path_prefix`、`credential_ref`、`tier`、`environment_id`、`application_id`、`connector_id`，`team_id` 可为空。`endpoint` 是 GitLab HTTPS 根地址，`source_url` 是同一主机上的仓库地址。启动时按实例 tier 选取绑定；无效或歧义配置拒绝启动。

凭据引用统一为 `vault://mount/path#field`，由现有 Vault KV v2 解析器按服务端绑定读取。还需在进程私有环境配置 `AIOPS_VAULT_URL`、`AIOPS_VAULT_TOKEN`。新请求中的旧 `vault:...` 格式会被拒绝；历史登记不会被迁移为未经核实的新凭据。不要把实际 Token、Vault 数据或 Git Markdown 正文放进仓库配置或日志。

## 接口与证据

| 操作 | 已实现语义 |
| --- | --- |
| `POST /api/v1/knowledge-sources/test` | 提交 URL、Vault 引用、应用 ID 和可选团队 ID；按当前应用/团队授权及存活范围匹配绑定，做即时只读探测，不登记、不保存健康或索引状态。凭据读取前记录探测尝试审计。 |
| `POST /api/v1/knowledge-sources` | 使用 `type=git_markdown`、显式 `application_id`、可选匹配团队、`schedule=manual`、空 `spaces`、`inherit_acl=true` 登记；事务中检查会话、应用映射、环境与知识管理授权，并写审计。登记不自动同步。 |
| `POST /api/v1/knowledge-sources/registrations/{id}/sync` | 人工触发一次读取；先检查已保存的配置指纹与当前绑定一致，再在远端读取前后核对应用/团队和授权，在事务中记录观察、绑定快照、固定提交 SHA、正文快照及审计。返回文档数和 `index_state=not_proven`。 |
| `GET /api/v1/knowledge-sources` | 只在最新读取的提交、文档正文哈希、观察和范围互相吻合时报告 `document_count`、`last_sync_at`；无证据为 `null`。连接健康、同步健康、片段数和向量索引状态仍未知。 |

辅助 `GET /api/v1/knowledge-sources/registrations/{id}/evidence` 在返回已保存的 Git 提交和文档数前，重新检查当前会话、应用/团队授权、精确服务端绑定、最新读取及正文证明；配置漂移或读取变化返回 `409 source_state_changed`。没有当前绑定时，不将历史读取当作现行来源证据。

Reader 核对 GitLab 项目 ID 与仓库 URL，解析配置分支对应的提交 SHA，再以该 SHA 遍历配置路径并读取 Markdown 原文，逐文件比对 Git tree blob ID。一次最多 500 个树条目、32 个 Markdown 文件，单文件 128 KiB，总正文 4 MiB；响应、分页和超时均有限制。完整空目录可形成真实的零文档读取；上游失败或范围超限不会提交部分结果。正文留在独立 V3 表中，不进入权限语义不同的旧 `knowledge_documents`。当前没有 Git Markdown 的向量分块或知识检索消费者，页面必须显示未索引/未知。

迁移 `124_v3_git_markdown_sources.sql` 增加来源类型和不可变读取/文档表。受控 GitLab/Vault 响应与隔离 PostgreSQL 测试只证明这条实现路径；真实服务连通与部署状态需另行验证。
