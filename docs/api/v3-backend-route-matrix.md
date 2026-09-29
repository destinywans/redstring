# V3 后端接口映射

由 `scripts/v3-route-matrix.py` 从 `v3-contract.md` 和 V3 接入前的 OpenAPI 生成段生成。
字段定义仍以 `web/src/v3/api/types.ts` 为准；状态仍以契约主表为准。
“旧路径适配”表示 URL 已存在，但必须替换为 V3 语义；“聚合”表示需要拼合多种真实记录；“新增”表示独立新能力。
这些类别不表示接口已实现或已通过数据库联调。

共 166 项：旧路径适配 6，聚合 30，新增 130。

| 分组 | 方法 | 路径 | 实施方式 | 契约状态 |
| --- | --- | --- | --- | --- |
| 身份与全局 | GET | `/me` | 新增 | implemented |
| 身份与全局 | PATCH | `/me` | 新增 | implemented |
| 身份与全局 | PATCH | `/me/preferences` | 新增 | implemented |
| 身份与全局 | GET | `/me/watched-services` | 新增 | implemented |
| 身份与全局 | GET | `/search` | 新增 | implemented |
| 身份与全局 | GET | `/mentions` | 新增 | implemented |
| 身份与全局 | GET | `/home` | 新增 | implemented |
| 身份与全局 | GET | `/mentions/{kind}/{id}/preview` | 新增 | implemented |
| 身份与全局 | GET | `/commands` | 新增 | implemented |
| 身份与全局 | GET | `/models` | 旧路径适配 | implemented |
| 身份与全局 | GET | `/models/admin` | 新增 | implemented |
| 身份与全局 | POST | `/uploads` | 新增 | implemented |
| 身份与全局 | GET | `/instance` | 新增 | implemented |
| 身份与全局 | GET | `/integrations/lower-sources` | 新增 | implemented |
| 身份与全局 | GET | `/config-sync` | 新增 | implemented |
| 身份与全局 | POST | `/config-sync/{id}/import` | 新增 | implemented |
| 身份与全局 | POST | `/promotions` | 新增 | implemented |
| 对话、画布与模板 | GET | `/conversations` | 聚合 | implemented |
| 对话、画布与模板 | GET | `/conversations/{id}` | 聚合 | implemented |
| 对话、画布与模板 | POST | `/conversations` | 新增 | implemented |
| 对话、画布与模板 | POST | `/conversations/{id}/messages` | 新增 | implemented |
| 对话、画布与模板 | GET | `/conversations/{id}/events` | 新增 | implemented |
| 对话、画布与模板 | GET | `/canvases` | 聚合 | implemented |
| 对话、画布与模板 | POST | `/canvases` | 新增 | implemented |
| 对话、画布与模板 | GET | `/canvases/{id}` | 聚合 | implemented |
| 对话、画布与模板 | POST | `/canvases/{id}/nodes/{node}/messages` | 新增 | implemented |
| 对话、画布与模板 | GET | `/canvas-templates` | 新增 | implemented |
| 对话、画布与模板 | POST | `/canvas-templates` | 新增 | implemented |
| 对话、画布与模板 | GET | `/investigations/{id}/canvas` | 新增 | implemented |
| 对话、画布与模板 | POST | `/investigations/{id}/branches` | 新增 | implemented |
| 对话、画布与模板 | PATCH | `/investigations/{id}/nodes/{node}` | 新增 | implemented |
| 对话、画布与模板 | POST | `/investigations/{id}/nodes/{node}/messages` | 新增 | implemented |
| 对话、画布与模板 | GET | `/investigations/{id}/writeback-preview` | 新增 | implemented |
| 对话、画布与模板 | POST | `/investigations/{id}/writeback` | 新增 | implemented |
| 对话、画布与模板 | GET | `/investigation-templates` | 新增 | implemented |
| 对话、画布与模板 | POST | `/investigation-templates/{id}/start` | 新增 | implemented |
| 事件与告警 | GET | `/inbox` | 聚合 | implemented |
| 事件与告警 | GET | `/alerts/{id}` | 聚合 | implemented |
| 事件与告警 | POST | `/alerts/{id}/claim` | 新增 | implemented |
| 事件与告警 | GET | `/alert-groups` | 聚合 | implemented |
| 事件与告警 | GET | `/alert-groups/{id}/policy` | 新增 | implemented |
| 事件与告警 | POST | `/alert-groups/bulk` | 新增 | mock |
| 事件与告警 | GET | `/services/{id}/peek` | 聚合 | implemented |
| 事件与告警 | GET | `/people/{name}/peek` | 新增 | implemented |
| 事件与告警 | GET | `/incidents` | 旧路径适配 | implemented |
| 事件与告警 | GET | `/incidents/{id}` | 旧路径适配 | implemented |
| 事件与告警 | POST | `/incidents/{id}/timeline` | 新增 | implemented |
| 事件与告警 | GET | `/alert-rules` | 新增 | implemented |
| 事件与告警 | GET | `/alert-rules/{id}` | 新增 | implemented |
| 事件与告警 | POST | `/alert-rules/{id}/dry-run` | 新增 | implemented |
| 事件与告警 | PUT | `/alert-rules/{id}` | 新增 | mock |
| 事件与告警 | GET | `/notification-routes` | 新增 | implemented |
| 事件与告警 | GET | `/silences` | 新增 | implemented |
| 事件与告警 | GET | `/inhibitions` | 新增 | implemented |
| 事件与告警 | POST | `/silences/preview` | 新增 | implemented |
| 事件与告警 | POST | `/silences` | 新增 | mock |
| 事件与告警 | DELETE | `/silences/{id}` | 新增 | mock |
| 事件与告警 | GET | `/proposals/{id}` | 新增 | implemented |
| 事件与告警 | POST | `/proposals/{id}/approve` | 新增 | mock |
| 事件与告警 | POST | `/proposals/{id}/reject` | 新增 | implemented |
| 事件与告警 | POST | `/proposals/{id}/comments` | 新增 | implemented |
| 事件与告警 | GET | `/executions/{id}` | 旧路径适配 | implemented |
| 事件与告警 | POST | `/executions/{id}/report/publish` | 新增 | implemented |
| 知识与记忆 | GET | `/runbooks` | 新增 | implemented |
| 知识与记忆 | GET | `/runbooks/{id}` | 新增 | implemented |
| 知识与记忆 | POST | `/runbooks/{id}/dry-run` | 新增 | implemented |
| 知识与记忆 | POST | `/runbooks/{id}/submit` | 新增 | implemented |
| 知识与记忆 | GET | `/postmortems` | 新增 | implemented |
| 知识与记忆 | GET | `/memory` | 新增 | implemented |
| 知识与记忆 | POST | `/memory/facts/{id}/{action}` | 新增 | implemented |
| 知识与记忆 | POST | `/memory/conflicts/resolve` | 新增 | implemented |
| 知识与记忆 | GET | `/knowledge-sources` | 聚合 | implemented |
| 知识与记忆 | POST | `/knowledge/search` | 旧路径适配 | implemented |
| 资源与观测 | GET | `/services` | 聚合 | implemented |
| 资源与观测 | GET | `/services/{id}` | 聚合 | implemented |
| 资源与观测 | GET | `/services/{id}/resilience` | 聚合 | implemented |
| 资源与观测 | POST | `/services/{id}/resilience/proposals` | 新增 | implemented |
| 资源与观测 | GET | `/topology` | 聚合 | implemented |
| 资源与观测 | GET | `/metrics/dashboard` | 聚合 | implemented |
| 资源与观测 | POST | `/metrics/ask` | 新增 | implemented |
| 资源与观测 | GET | `/traces` | 聚合 | implemented |
| 资源与观测 | GET | `/traces/{id}` | 聚合 | implemented |
| 资源与观测 | GET | `/services/discover` | 新增 | implemented |
| 资源与观测 | POST | `/services` | 新增 | implemented |
| 资源与观测 | GET | `/logs` | 聚合 | implemented |
| 资源与观测 | GET | `/changes` | 聚合 | implemented |
| v4 新建流程 | GET | `/incidents/draft` | 新增 | implemented |
| v4 新建流程 | POST | `/incidents` | 新增 | implemented |
| v4 新建流程 | POST | `/alert-rules/generate` | 新增 | implemented |
| v4 新建流程 | POST | `/alert-rules/preview` | 新增 | implemented |
| v4 新建流程 | POST | `/alert-rules` | 新增 | mock |
| v4 新建流程 | POST | `/alert-rules/import` | 新增 | mock |
| v4 新建流程 | POST | `/notification-routes/test` | 新增 | implemented |
| v4 新建流程 | POST | `/notification-routes` | 新增 | implemented |
| v4 新建流程 | POST | `/inhibitions/preview` | 新增 | implemented |
| v4 新建流程 | POST | `/inhibitions` | 新增 | implemented |
| v4 新建流程 | GET | `/runbooks/draft` | 新增 | implemented |
| v4 新建流程 | POST | `/runbooks` | 新增 | implemented |
| v4 新建流程 | GET | `/postmortems/candidates` | 新增 | implemented |
| v4 新建流程 | GET | `/postmortems/draft` | 新增 | implemented |
| v4 新建流程 | POST | `/postmortems` | 新增 | implemented |
| v4 新建流程 | POST | `/knowledge-sources/test` | 新增 | implemented |
| v4 新建流程 | POST | `/knowledge-sources` | 新增 | implemented |
| Agent 链路 | GET | `/agent-runs/{id}/trace` | 聚合 | implemented |
| 集成中心 | GET | `/connectors` | 新增 | implemented |
| 集成中心 | GET | `/connectors/{id}` | 新增 | implemented |
| 集成中心 | POST | `/connectors/{id}/test` | 新增 | implemented |
| 集成中心 | PATCH | `/connectors/{id}` | 新增 | implemented |
| 集成中心 | POST | `/connectors` | 新增 | implemented |
| 集成中心 | GET | `/connector-types/{type}` | 新增 | implemented |
| 集成中心 | GET | `/mcp-servers` | 新增 | implemented |
| 集成中心 | GET | `/mcp-servers/{id}` | 新增 | implemented |
| 集成中心 | PATCH | `/mcp-servers/{id}/tools/{name}` | 新增 | implemented |
| 集成中心 | POST | `/mcp-servers/{id}/review` | 新增 | implemented |
| 集成中心 | GET | `/ingest/pull/{id}` | 新增 | implemented |
| 集成中心 | POST | `/ingest/pull/{id}/test` | 新增 | implemented |
| 集成中心 | GET | `/ingest/push` | 聚合 | implemented |
| 集成中心 | POST | `/ingest/push/test` | 新增 | implemented |
| 集成中心 | GET | `/service-mappings` | 聚合 | implemented |
| 集成中心 | POST | `/service-mappings/accept` | 新增 | implemented |
| 集成中心 | POST | `/service-mappings/discover` | 新增 | implemented |
| 集成中心 | PUT | `/service-mappings` | 新增 | implemented |
| 集成中心 | GET | `/coverage` | 聚合 | implemented |
| 集成中心 | GET | `/webhooks` | 聚合 | implemented |
| 集成中心 | POST | `/webhooks/{id}/redeliver` | 新增 | mock |
| 集成中心 | POST | `/api-tokens` | 新增 | mock |
| Agent 运行台 | GET | `/agents/lifecycle` | 新增 | implemented |
| Agent 运行台 | GET | `/agents/{id}` | 新增 | implemented |
| Agent 运行台 | POST | `/agents/{id}/rollout` | 新增 | implemented |
| Agent 运行台 | POST | `/agents/{id}/rollback` | 新增 | implemented |
| Agent 运行台 | PATCH | `/agents/{id}/level` | 新增 | implemented |
| Agent 运行台 | GET | `/prompts` | 新增 | implemented |
| Agent 运行台 | GET | `/prompts/{id}` | 新增 | implemented |
| Agent 运行台 | POST | `/prompts/{id}/trial` | 新增 | implemented |
| Agent 运行台 | POST | `/prompts/{id}/versions` | 新增 | implemented |
| Agent 运行台 | GET | `/agent-tools` | 新增 | implemented |
| Agent 运行台 | GET | `/evals/{id}` | 新增 | implemented |
| Agent 运行台 | POST | `/evals` | 新增 | implemented |
| Agent 运行台 | GET | `/eval-cases/{id}` | 新增 | implemented |
| Agent 运行台 | POST | `/eval-cases/{id}/labels` | 新增 | implemented |
| Agent 运行台 | GET | `/agent-runs` | 聚合 | implemented |
| Agent 运行台 | GET | `/agent-runs/{id}` | 聚合 | implemented |
| Agent 运行台 | POST | `/agent-runs/{id}/feedback` | 新增 | implemented |
| Agent 运行台 | GET | `/guardrails` | 新增 | implemented |
| Agent 运行台 | PATCH | `/guardrails/{id}` | 新增 | implemented |
| 设置 | GET | `/me/profile` | 聚合 | mock |
| 设置 | PUT | `/me/profile` | 新增 | mock |
| 设置 | DELETE | `/me/sessions/{id}` | 新增 | implemented |
| 设置 | GET | `/members` | 聚合 | implemented |
| 设置 | POST | `/members/invitations` | 新增 | implemented |
| 设置 | POST | `/members/sync` | 新增 | mock |
| 设置 | POST | `/members/mfa-reminders` | 新增 | mock |
| 设置 | GET | `/roles` | 新增 | implemented |
| 设置 | GET | `/roles/{id}` | 聚合 | implemented |
| 设置 | PUT | `/roles/{id}` | 新增 | implemented |
| 设置 | GET | `/security` | 聚合 | mock |
| 设置 | PUT | `/security` | 新增 | mock |
| 设置 | GET | `/me/notification-settings` | 新增 | implemented |
| 设置 | PUT | `/me/notification-settings` | 新增 | implemented |
| 设置 | GET | `/me/ai-preferences` | 新增 | implemented |
| 设置 | PUT | `/me/ai-preferences` | 新增 | implemented |
| 设置 | GET | `/workspace` | 新增 | implemented |
| 设置 | PUT | `/workspace` | 新增 | implemented |
| 设置 | GET | `/audit` | 旧路径适配 | implemented |
| 设置 | GET | `/audit/{id}` | 聚合 | implemented |
| 设置 | POST | `/audit/export` | 新增 | implemented |
