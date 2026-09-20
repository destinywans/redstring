# WAN-77：知识治理与检索契约

本文件对应 2026-09-20 批准的多应用 SRE 工作区方案。知识原文、版本和来源在 PostgreSQL 中；向量是可重建派生物。实现位置：`internal/knowledge`。

## 生命周期与来源

`Document.id` 是稳定文档 ID，`version` 从 1 递增。`PUT` 必须提供 `expected_version`，竞争编辑返回 409；每次编辑生成不可变原文快照，并回到 `draft`。旧引用使用 `document_id + version` 获取对应内容，向量代次同时绑定版本，旧构建不能覆盖新原文的索引。

`lifecycle` 为 `draft → review → published`，各状态可归档，归档可回到草稿；`status` 仍只表示索引状态。只有当前发布版本参与检索。编辑已发布文档后，当前草稿暂不参与检索；旧引用仍可读取。恢复旧稿需要编辑为新版本并重新审阅发布，不能覆盖历史。

元数据字段：`maintainer_id`、`team_id`、`origin`（human/agent/historical_unknown）、`actor_id`、`generation_run_id`、`generation_model`、`evidence_ids`、`environment_ids`、`resource_categories`、`tags`、`shared_application_ids`、`visibility`（application/workspace）。人工创建接口强制记录 human，禁止客户端伪造 Agent 来源。修订保留原始来源；修订操作者和生命周期操作者另记审计。

初始迁移保留历史文档 ID、版本和引用；未知来源标记 historical_unknown，不臆造 Agent 或人工身份。旧全局资料保留工作区可见性；之后新增工作区公开资料必须由管理员操作。迁移按幂等 DDL 执行，在模块 `Migrate` 中，不是纯文件声明。

## 权限与检索

`Service.Register` 接入工作区应用授权，HTTP 的用户与后台 Worker 的 `store.Principal` 共用同一检查。应用 ACL 与知识适用范围分开：列举、直接读取、版本、导出、检索和修改均检查权限。分享其他应用必须对源与接收应用有操作权限；维护人需要源应用操作权限。旧快照也单独检查可见范围，后续分享不自动公开全部历史版本。

查询先限定授权应用、显式共享/工作区资料、环境与发布状态。关键词涵盖标题、标签、正文及错误码，中文连续字符添加二元词；向量按当前 Embedding 身份匹配。通过 RRF 融合、同版本同块去重并限制总上下文量。向量故障降级关键词，返回 `mode=keyword, degraded=true`；工具取证也保留该标识。关键词实现为 PG 过滤及有界输出、非大规模全文索引；大量文档时需重新评估执行成本。

Markdown 按标题切分，超长章节以 Unicode 字符边界切片并重叠。Embedding 变更不会误用旧向量，但仍可检索关键词；重新索引不改原文。

## REST

保留 `/api/v1` 前缀。

| 方法与路径 | 内容 |
| --- | --- |
| GET `/knowledge/templates` | `items: [{id,title,body}]`；blank / investigation / change-recovery |
| POST `/knowledge` | 原 Document 导入字段 + metadata；创建人工草稿 |
| PUT `/knowledge/{id}` | `{expected_version,title,body,source,metadata}` |
| POST `/knowledge/{id}/transition` | `{expected_version,state}`；review/published/archived/draft |
| GET `/knowledge/{id}/revisions` | `items: Document[]`，最新在前 |
| GET `/knowledge/{id}/revisions/{version}` | 不可变内容快照 |
| GET `/knowledge/{id}/export` | `{format:"aiops-knowledge-v1",document_id,exported_at,document,revisions}` |
| POST `/knowledge/search` | `{application_id,environment_id?,query,limit?}` → `{items,mode,degraded,reason?,embedding?}` |
| POST `/investigations/{id}/knowledge-drafts/generate` | 生成一次经验草稿；重复请求返回已有草稿 |

原 list/get/reindex、人工经验 draft/confirm 保留。经验确认是显式人工审核发布入口，必须调查关闭且证据属于该调查。普通 Runbook 经过 review 再 publish。

## 经验生成集成

注入 `Service.ExperienceGenerator(ctx, ExperienceInput) (GeneratedExperience,error)`；输入为已关闭调查、成功恢复结果和有界已提交证据；输出为 DraftInput + generation RunID/Model。不注入时明确返回依赖失败，不生成假模型内容。

自动入口由调用者在成功关闭后调用 `GenerateExperienceDraft(ctx, investigationID, userID, true)`，并注入 `AutoExperienceEnabled(ctx, investigationID)`。策略默认关闭。手动入口不绕过权限与闭环证据要求。持久租约防重入，生成至多 2 分钟，保留失败状态；显式重试可接管过期租约。完成时再次检查权限、调查状态、租约 token 与证据归属，并在同一事务中写草稿与完成标记。每个调查最多一份自动/手动生成的经验草稿；人工编辑草稿是另一个已有入口。

这里提供生成与一次性持久化机制；调度、选模型、环境策略和关闭事件的组合由服务入口负责。模型返回的原文只是待审草稿，绝不自动发布。

## 验证边界

定向验证包含 Unicode 分块/错误码、融合去重、便携仓储接口，以及随机隔离 PG schema 下的迁移、应用 ACL、草稿排除、人工发布、Embedding 故障降级、不可变引用、过期修订和导出。没有调用真实模型或现有业务数据，没有运行全量测试。
