# WAN-93 产品 AI 扩展

本模块管理产品使用的 MCP 客户端、Skills 和 Prompt 模板，与 Codex 开发技能无关。

## 组合入口

```go
extensions := aiextensions.New(repository.DB, os.Getenv("AIOPS_MCP_CREDENTIAL_ROOT"))
if err := extensions.Migrate(ctx); err != nil { /* fail startup */ }
extensions.Register(api) // 绑定已有应用 ACL；未绑定时默认拒绝访问
// 在 worker.Tools(ctx, app) 中合并 extensions.Tools(ctx, app)。
// MCP 最多占 8 个工具；与原有工具合计不得超过 inference 的 16 个上限。
```

迁移需在基础 Store.Migrate 完成后执行，新增 `ai_extensions` 与 `ai_extension_revisions`。版本、当前记录及管理审计在同一个 PG 事务提交；更新用提交者持有的 version 做 CAS。接口层不允许迁移记录到其他应用或改变类别。停用形成新版本，历史保留。

前端默认导出 `web/src/features/aiextensions/ExtensionsPage.tsx`。挂载 `/ai-extensions/skills`、`/ai-extensions/prompts`、`/ai-extensions/mcp`。页面中选择适用应用；新调查必须显式选择环境。

## HTTP 契约

所有路径前缀 `/api/v1`，使用现有会话认证、角色及应用 ACL：

| 方法、路径 | 请求 / 响应 | 权限 |
| --- | --- | --- |
| GET `/ai-extensions?application_id=&kind=skill\|prompt\|mcp` | `{items: Record[]}` | view；mcp 另需 admin |
| GET `/ai-extensions/examples` | 未保存的示例 `{items: Record[]}` | viewer |
| GET `/ai-extensions/mcp-capabilities` | transport、credential_storage_configured、fixed_arguments_only、maximum_tools；不包含私有目录路径 | admin |
| GET `/ai-extensions/{id}?version=整数` | 指定版本；省略表示当前 | view；mcp 另需 admin |
| POST `/ai-extensions` | Record，id 空、version 0 → v1 | operate；mcp 另需 admin |
| PUT `/ai-extensions/{id}` | Record，version 为期望当前版本 → 新版本 | 同上 |
| POST `/ai-extensions/compose` | `{application_id,references:[{id,version}],question}` → `{content,references}` | operate |
| POST `/ai-extensions/{id}/discover` | `{items:[{name,description,read_only_hint,input_schema}]}` | admin |
| POST `/ai-extensions/{id}/test` | `{binding:登记名}` → Evidence | admin |

Record 字段见 `types.go`。失败区分 403 权限、404 不存在、409 版本冲突、422 输入或必需工具缺失、503 外部依赖不可用。不把远端错误正文或凭据内容返回页面。

组合结果包含 ID、版本、SHA256、维护人、完整已选文本。页面先预览，再带入新调查或已有节点草稿；用户确认发送后，通过现有消息接口把完整组合内容持久化。不会悄悄修改 system prompt，不会直接执行技能。这个出处是用户消息中的文本记录，不是不可篡改的结构化审计字段；用户仍可在发送前编辑草稿。

## MCP 边界

使用 [官方 Go SDK](https://github.com/modelcontextprotocol/go-sdk) v1.7.0 的 [StreamableClientTransport / ClientSession](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp)，由 SDK 完成初始化、协议协商、工具发现和调用。`DiscoverCatalog` 在同一受限会话中观察服务版本及已宣告能力，仅调用 tools/list、resources/list、prompts/list，并有时间、分页、项目数和大小上限。未宣告的能力标为 unsupported；已宣告却返回空列表才是观测到的零项。首版不启动 stdio 子进程，不实现 OAuth 授权流程，不调用 prompts/get、resources/read、远程 Prompt 执行、资源链接解引用、sampling 或 elicitation。目录观察本身不授予 V3 Agent 使用权限。

- 管理员按应用登记 endpoint、固定参数的只读工具白名单及凭据文件引用；MCP 服务必须由管理员确认可信且凭据本身应只读。服务的 readOnlyHint 只是额外校验，不能单独作为权限授权。
- HTTP 仅允许显式开启的回环地址；其他 endpoint 必须 HTTPS。禁止 URL 用户信息、query、fragment、重定向；凭据只向登记的精确 URL 发送。
- `AIOPS_MCP_CREDENTIAL_ROOT` 指定私有目录，credential_ref 是目录内单个文件名。token 文件必须是普通文件、非符号链接、0600 或更严格、最多 8 KiB；内容为单行 Bearer token。API 从不返回文件内容。
- 每条 binding 的 `arguments` 完全固定。Agent 的 EvidenceTool `query` 只接受空串或 `{}`，不能把 app、URL、查询表达式替换成别的目标。动态参数需要未来增加明确的范围约束，不在首版隐式开放。
- 调用前重新核验应用 ACL、当前配置版本和启用状态，再发现并检查只读提示；正在使用的旧版本被编辑或停用后，后续调用被拒绝。已经发出的远程调用不能被这些检查追溯撤销。
- 请求时间和响应大小有界，只收集文本与 structuredContent，不解引用资源链接或下载远端图片。证据携带 extension_id、version、binding 与采集时间，沿用既有证据持久化路径。
- 官方协议依赖与产品适配有真正的本机 HTTP 集成测试；测试服务器是专用合成服务，不冒充真实业务 MCP 平台。

## 定向检查

`go test ./internal/aiextensions` 覆盖真实协议 initialize/list/call、固定参数、跨应用权限、只读门槛、旧配置撤销、凭据权限、版本组合。传入本机 `TEST_DATABASE_URL` 后另建随机 schema，验证迁移、CAS、历史重读与同事务审计；清理只删除该随机 schema。`pnpm --dir web build` 检查受影响前端类型和构建，不能替代页面视觉验收。
