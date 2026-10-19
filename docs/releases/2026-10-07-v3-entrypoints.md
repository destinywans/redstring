# V3 正常使用入口交付（2026-10-07）

用户批准优先补齐成员授权、MCP 登记、调查与知识只读工具接线；记忆生产、评测执行与 Agent 执行治理留作后续待办。本批仅后端，三个需求分别为 WAN-237、WAN-238、WAN-239。

分支 `codex/wan-117-v3-entrypoints`，依赖已推送基线 `1e34477f31a50f2f0c226bd5ddbe180a7ecd519e`。实现按三个需求分项提交；精确提交、远端核验和当前验收状态见对应 Linear 交付记录。本文件不代表已合并、部署或整体验收。

## 完成的功能

| 需求 | 入口和结果 | 使用前提与边界 |
| --- | --- | --- |
| WAN-237 | 创建自定义业务角色；读取完整成员授权快照；按成员/角色版本分配直接角色及启停；授权变更撤销旧会话 | 当前人类管理权限、明确环境范围；保护末位可登录管理员，保留继承权限。目录授权不建立 SSO 或初始化口令；环境角色的完整范围编辑器另列 |
| WAN-238 | 首次登记 MCP 应用/环境配置，随后可进入已有发现、人工版本审核和工具开关流程 | 部署须通过私有配置批准精确端点及凭据引用；登记不请求网络、不批准或执行工具。POST 每次新建独立记录，不提供重试去重 |
| WAN-239 | 调查节点接入服务资料、指标、日志元数据、知识四类只读工具；普通 Chat 接入按精确服务归属的知识检索 | 模型须显式获准且支持工具，连接器和知识来源须可用；调查固定持久应用/环境、最多四次工具和五次模型调用、总预算 90 秒；成功证据与回复原子提交 |

详细契约：[成员授权](../api/v3-member-access.md)、[MCP 登记](../api/v3-mcp-registration.md)、[调查与知识工具](../api/v3-investigation-evidence-tools.md)。MCP 的启动配置为 `AIOPS_V3_MCP_REGISTRATION_BINDINGS`，文件内容和权限要求见登记契约；配置不含凭据正文。本批没有新增数据库迁移。

## 已执行验证

- 定向 Go 单测：`v3admin`、`v3mcp`、`v3canvas` 在未设置 `TEST_DATABASE_URL` 时通过，PG 场景明确跳过；组合根 `TestV3ReadToolsPreserveModelAndInvestigationScope`、`TestV3ChatKnowledgeResolvesAuthorizedApplicationBeforeReading`、`TestV3MCPRegistrationBindingsRequireExactDeploymentAdmission` 通过。
- 随后在本批独占、loopback 暴露、tmpfs 存储的 PostgreSQL 17 + pgvector 临时容器内实际运行 **15 个不同的顶层 PG/运行链测试**，最终全部通过。每个测试使用随机 schema 并执行所需既有迁移；没有对主业务库或现有 testlab 写入。
- 新入口的 6 个测试：`TestRoleCreationMemberAccessHTTPPostgres`、`TestMemberAccessSyncedActivationAndLastPlatformAdminPostgres`、`TestMemberAccessAuthorityAndLastManagerPostgres`、`TestRoleCreationRechecksAfterLockPostgres`、`TestMCPRegistrationHTTPIsolated`、`TestInvestigationEvidenceRegisteredRoute`。覆盖真实会话/授权、跨环境拒绝、CAS、等待锁时撤权、末位管理员保护、审计回滚、MCP 准入与完整元数据流程、调查证据成功及撤权/下架/跨应用拒绝。
- 9 个受影响回归：正式启动组装的 MCP 路由、旧 MCP 目录审核开关、角色矩阵写入、调查节点回复与回写、知识工具的三个范围/撤权测试、普通对话工具流。正式组装的 MCP 测试经新增 POST 登记连到本地 MCP SDK 协议服务，完成发现/审核/开关；工具执行 handler 未被调用。
- 模型、遥测和知识工具的运行测试使用受控进程内或本地 HTTP 适配器；上述 PG 和 MCP 协议属于真实本地运行证据，不表示外部企业服务或真实付费模型验收。
- `python3 scripts/build-openapi.py` 生成 337 paths / 411 operations / 505 schemas；`check-contract.py` 校验 411 个源码操作匹配，`check-v3-contract.py` 校验冻结页面契约仍为 165 行（另含 SSE 的历史 166 口径）。新辅助入口不用于增加该旧计数。`git diff --check` 通过。
- 独立只读审查涵盖身份/会话锁、范围、来源复核、工具事件顺序与事务提交；交付时无确认且未修复的问题。审查不代替实际部署验收。

本批早期失败保留于忽略目录 `.local/entrypoints-20261007/`：MCP 测试 recorder 不支持写超时、toggle 夹具缺少现有版本/hash、目录用户夹具缺少现有必填 external_id。修正夹具后复验通过，未放松生产权限或版本检查。另在组合根审查中发现服务 ID 是编码后的应用/环境元组，已按真实编码解析并增加输入边界验证。

测试数据库清理已核：没有残留测试 schema，精确删除本批独占容器，临时端口关闭；本地凭据清单已去除临时连接串。历史失败日志保留，不纳入 Git。

## 剩余工作

已按用户决定在 WAN-123 留作后续：

- 记忆事实/冲突的真实生产链。
- Eval 执行器、用例输入、快照与结果生产，以及 Prompt 试运行所需真实用例来源。
- Agent 灰度/回滚、等级、护栏等执行治理与运行闭环。

这批后端入口已具备实现和定向验证；前端按钮/表单接线、实际组织认证、部署侧 MCP 准入与真实外部来源/模型仍需相应环境验收。全接口、完整业务链、性能与连续 24 小时状态不因本批通过而升级。没有恢复前端、原生 CI/CD、Workflows、VM、故障注入或外部通知。
