# Skills / Prompt 适用范围

2026-09-20 用户反馈：通用技能和 Prompt 不应被“先选择应用”挡住。通用和应用内容共用管理界面；全局环境用于资源查看，不能把通用模板隐式绑定到环境。

## 范围与权限

- `application_id: ""` 表示工作区通用记录，仅支持 `skill` / `prompt`。
- 通用记录由已登录的管理员、操作员、只读成员读取；仅管理员新建或更新。
- 非空 `application_id` 为应用专属记录，读取沿用该应用 `view`，写入沿用 `operate`，只读角色不能写。
- MCP 仍必须绑定具体应用，只有管理员可以管理，不能新建通用 MCP 或借通用内容扩大工具权限。
- 已有记录的应用 ID 和 kind 均不可修改，包括通用转应用或应用转通用。调整范围需复制为新记录，产生新的 ID 和版本历史。
- 通用内容不存放生产秘密或特定应用证据，因为所有工作区成员都可以阅读。

## 兼容接口

- `GET /api/v1/ai-extensions?application_id=<id>&kind=skill|prompt|mcp`：原语义，仅该应用记录，不自动混入通用。
- `GET /api/v1/ai-extensions?scope=workspace&kind=skill|prompt`：只返回通用记录；不能同时提供非空 `application_id`。
- 明确 `scope=application` 等价于原应用列表；其他 scope 返回 422。
- 列表响应追加 `can_write: boolean`，表示当前用户对该列表范围的写权限。原 `items` 消费保持兼容。
- 新建通用内容向原 POST 接口提交空 `application_id`；编辑走原 PUT，版本冲突和审计继续保留。
- 界面默认聚合通用查询与已有可访问应用的单应用查询。每个应用请求仍独立验证服务端 ACL，失败不丢弃其他成功结果。每个范围当前最多加载 200 条，界面数量仅表示已加载记录。

## 组合与会话

`POST /api/v1/ai-extensions/compose` 仍要求具体 `application_id` 及应用 `operate` 权限；允许引用通用或同应用的 Skill / Prompt。

- 其他应用的内容不能跨应用组合。
- 记录必须当前启用，指定版本必须存在。
- 所需 MCP 工具必须出现在目标应用已启用的登记工具中；通用记录不提供新的工具权限。
- 用户仍需预览、明确选择目标应用及环境/会话节点，再确认发送。原始内容快照、版本、hash 沿现有会话记录保存。
- 内置示例未保存时只是示例。管理员可导入通用或有权限的应用；操作员只可导入有操作权限的应用。

## 存储迁移与发布注意

模块 `PGRepository.Migrate` 在原有锁和事务中执行可重复迁移：移除 `ai_extensions.application_id` 的 NOT NULL，保留外键；增加 CHECK 约束，仅 Skill / Prompt 允许数据库 NULL。

外部 API 仍用空字符串表达通用，PG 写入用 NULLIF 映射，列表/更新通过 `IS NOT DISTINCT FROM` 匹配，原应用记录及版本无需搬迁。审计表继续用空 application_id 记录工作区行为。

2026-09-20 已在本机 18081 预览服务启动时执行迁移，保留既有应用记录。预览二进制采用受影响模块源码快照，未注册暂停中的 CI/CD 路由与 Worker。无须创建虚构全局应用。

## 已执行的定向检查

`go test ./internal/aiextensions -run '^(TestPostgresVersionCASAuditAndReload|TestWorkspaceScopeReadWriteAndImmutableScope|TestWorkspaceCompositionRequiresTargetAndTargetTools)$' -count=1`

两项内存测试覆盖通用读写、匿名拒绝、范围不可移动、MCP 非通用、旧列表隔离、组合目标授权、目标工具要求和停用阻断。PG 定向测试在独立随机 schema 中验证通用记录的创建、版本更新、应用列表隔离及数据库约束，结束清理该 schema。三项通过；受影响前端构建通过。没有调用真实模型或外部 MCP 工具。浏览器确认默认列表无需选择应用、通用/应用新建表单和权限提示；尚未做完整业务回归。
