# V3 配置提升：接线与证据边界

配置提升通过**独立 GitLab 配置仓库**传递，不在生产实例与非生产实例之间直接调用。当前后端仅支持把非生产实例中本人所有的**已净化画布模板**导出为签名清单和 MR，再由生产实例显式导入。Runbook、告警规则与 Agent 等页面选项尚未接通，相关请求明确返回 `501 promotion_kind_unavailable`；三条 V3 主页面接口仍为 `mock`。

## 私有配置

可选环境变量 `AIOPS_V3_CONFIG_SYNC_CONFIG` 指向绝对路径的单个 JSON 文件。文件须为普通文件、权限 `0600`、最大 16 KiB；不设置时路由仍注册，但返回 `501 config_repository_unavailable`。占位格式分别见 [非生产示例](../examples/v3-config-sync.nonprod.json) 和 [生产示例](../examples/v3-config-sync.production.json)。`tier` 必须与当前平台实例一致。示例中的域名、项目 ID 与路径不能直接用于运行。

- `gitlab` 固定 HTTPS 基址、正整数项目 ID、目标分支和提升分支前缀；仅在显式测试配置中允许 loopback HTTP。`token_file` 为绝对路径的普通 `0600` 文件。配置文件只写令牌文件路径，不写令牌。
- 非生产实例配置一个 `source_instance_id` 和 `signing_key_file`。签名密钥文件为绝对路径、普通 `0600` 文件，内容是无填充 base64url 编码的 64 字节 Ed25519 私钥；不配置 `trusted_sources`。
- 生产实例配置 1–32 个明确来源 ID 到公钥文件绝对路径的映射，不配置签名私钥。公钥文件内容是无填充 base64url 编码的 32 字节 Ed25519 公钥，不能允许组或其他用户写入。来源信任只来自此服务端映射，不从 MR 自动发现。

配置仓库应只用于这类配置清单，目标分支须禁止直接推送与强推，并设置独立人员审核。令牌、密钥与真实配置留在私有挂载中；不要放入 `.env`、仓库示例、审计或运行日志。

## 操作与验证

`POST /promotions` 只在一个选定的非生产环境接受本人模板及当前 `canvas.change` 授权，生成固定分支/路径的有界签名清单并提交 MR。清单只含受限节点和边结构，不携带调查执行记录、证据、凭据或账号。导出记录保存在 `v3_config_exports`；相同来源摘要复用固定身份。远端结果不确定时返回 503，不能把本地预留记录视为已建 MR。租约代次阻止过期请求修改后来请求的记录和审计；租约过期后仍可能发生同一远端身份的并发写入并产生需重试对账的冲突。

生产 `GET /config-sync` 最多列 100 条**候选元数据**，尚未逐条验证签名和完整来源。`POST /config-sync/{id}/import` 只接受 `mr-<正整数 IID>`，要求当前人类平台管理员和全局 `workspace.manage`，写入事务内再次核权；导入与审计共同提交。服务端核对已合并 MR、固定提交与唯一预期文件、来源签名和模板白名单，并校验独立审批与当前目标分支保护证据。当前 API 查询无法证明**合并当时**的审批/保护策略；真实配置仓库上线前仍需保留治理配置和审查证据。不可验证的来源返回 503，不会导入部分配置；重复相同导入收敛，目标已变化返回 409。

数据库迁移依次使用 `120_v3_config_promotion.sql` 与 `121_v3_config_export_attempt_fence.sql`。当前仅在一次性隔离 PostgreSQL、替身 GitLab 和定向 HTTP 测试中验证；没有对现有业务库执行迁移、连接真实配置仓库或验收前端页面。`GET /config-sync`、`POST /promotions`、`POST /config-sync/{id}/import` 的精确请求、响应和错误见 [V3 页面契约](v3-contract.md) 与 [OpenAPI](../../internal/apicontract/openapi.json)。
