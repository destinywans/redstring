# V3 配置提升：接线与证据边界

配置提升通过**独立 GitLab 配置仓库**传递，不在生产实例与非生产实例之间直接调用。领域层现有已净化画布模板、Runbook、告警规则与 Agent 版本四类列表、导出及已合并导入流程。四类写入链本次只完成源码，未运行新的测试、迁移或真实环境联调；主契约生成、告警来源注入与部署接线由集成会话处理，冻结页面也尚未验收。未配置仓库、签名信任或告警来源时返回明确的未接入错误。

## 私有配置

可选环境变量 `AIOPS_V3_CONFIG_SYNC_CONFIG` 指向绝对路径的单个 JSON 文件。文件须为普通文件、权限 `0600`、最大 16 KiB；不设置时路由仍注册，但返回 `501 config_repository_unavailable`。占位格式分别见 [非生产示例](../examples/v3-config-sync.nonprod.json) 和 [生产示例](../examples/v3-config-sync.production.json)。`tier` 必须与当前平台实例一致。示例中的域名、项目 ID 与路径不能直接用于运行。

- `gitlab` 固定 HTTPS 基址、正整数项目 ID、目标分支和提升分支前缀；仅在显式测试配置中允许 loopback HTTP。`token_file` 为绝对路径的普通 `0600` 文件。配置文件只写令牌文件路径，不写令牌。可选 `ca_file`（PA-91）用于内部 CA 签发证书的 GitLab：须为绝对路径的普通文件（非符号链接）、组和其他用户不可写、最大 1 MiB、内容为至少一张 PEM 证书，且只能配合 HTTPS；配置后该客户端只信任这份 CA（不再叠加系统根证书），仍按 `base_url` 校验主机名，只对配置提升及其派生的通知/抑制/告警规则草稿 GitLab 客户端生效，不影响进程内其他 TLS 连接。不需要时省略或留空；示例中的路径为占位。可选 `web_url`（PA-98）是 GitLab 的 external_url，用于 API 走代理、网页链接在另一地址的部署（例如验收环境 API 为 `https://127.0.0.1:18446`、网页为 `http://127.0.0.1:28080`）：须为 `http` 或 `https`、带主机、无用户信息、查询、片段或路径。配置后 MR 链接必须与它的协议和主机一致，否则与 `base_url` 一致；链接仍不得带用户信息或片段。链接不一致时返回 `503 gitlab_web_url_mismatch`，导出记为结果未知（`v3.config_export.result_unknown`、`uncertain`）；修正 `web_url` 后用同一对象重新导出，按同一分支对账并复用已建的 MR，不再新建。生产侧读取已合并 MR 时同样返回这个错误码。
- 非生产实例配置一个 `source_instance_id` 和 `signing_key_file`。签名密钥文件为绝对路径、普通 `0600` 文件，内容是无填充 base64url 编码的 64 字节 Ed25519 私钥；不配置 `trusted_sources`。
- 生产实例配置 1–32 个明确来源 ID 到公钥文件绝对路径的映射，不配置签名私钥。公钥文件内容是无填充 base64url 编码的 32 字节 Ed25519 公钥，不能允许组或其他用户写入。来源信任只来自此服务端映射，不从 MR 自动发现。

配置仓库应只用于这类配置清单，目标分支须禁止直接推送与强推，并设置独立人员审核。令牌、密钥与真实配置留在私有挂载中；不要放入 `.env`、仓库示例、审计或运行日志。

## 操作与验证

`POST /promotions` 在单个非生产环境接受四类对象 ID。模板沿用本人所有权及 `canvas.change`；Runbook 读取当前已发布版本并要求应用与所属团队的 `knowledge.manage`；告警规则从受信 Prometheus 来源及精确规则组绑定读取，要求应用或所属团队的 `alert_rules.manage`；Agent 读取当前稳定目录版本并要求 `agents.change`。后三类清单明确记录来源对象版本、来源实例/环境、应用、净化快照摘要和 Ed25519 签名。Runbook 仅保留标题、触发提示及步骤结构，不带正文、代码、工具或连接器；告警仅保留受限表达式、时长与允许的标签，不带注解或运行态；Agent 仅保留名称、版本和摘要固定的制品引用，不带提示词或工具。制品引用不证明远端字节或仓库真实性。自由文本虽受限，仍须在 MR 中人工检查敏感内容。模板清单仍含用户提交的名称、模板类型和净化图结构；前端可能默认取源画布标题，导出前须检查。导出记录保存在 `v3_config_exports`；相同来源摘要复用固定身份。远端结果不确定时返回 503，不能把本地预留记录视为已建 MR。租约代次阻止过期请求修改后来请求的记录和审计；租约过期后仍可能发生同一远端身份的并发写入并产生需重试对账的冲突。

导出前校验（PA-96、PA-97）：来源对象超出导出契约时，在预留导出记录和访问 GitLab 之前返回 `422`，错误码说明原因，不再报成 `503 config_source_untrusted`。告警规则的原因只在调用方通过该规则应用（或所属团队）的 `alert_rules.manage` 之后给出，否则仍是授权错误。

| 错误码 | 原因 |
| --- | --- |
| `template_has_description` | 模板带说明文字；清单只携带结构 |
| `template_retained_content` | 模板保留了节点标题、步骤或内容（保存时勾选了保留步骤/内容且源画布有这些内容） |
| `template_private_runbooks` | 模板关联了本实例的 Runbook 版本，生产实例无法对应 |
| `template_structure_invalid` | 节点、连线或 `private_template_v1` 有导出契约以外的字段或取值 |
| `alert_rule_expression_unsafe` | 表达式含标签匹配值以外的字符串字面量，或匹配值不只由 `[A-Za-z0-9._:-]` 组成，或匹配的标签名像凭据（token、secret、password、credential、api_key、auth） |
| `alert_rule_label_not_exportable` | 标签不只是 `environment`、`application` 和 `severity`，或级别不在 `P1`–`P4`、`info`/`warning`/`critical` 之内 |
| `alert_rule_not_exportable` | 名称、分组或持续时间超出范围 |
| `artifact_ref_not_pinned` | Agent 稳定版本的制品引用没有固定到 `@sha256:<64 位十六进制>`；注册时不检查，目录允许标签引用 |
| `application_key_missing` | 应用没有 key（`applications.key` 为空），生产无法对应（PA-100） |

两端用应用 key 对应（PA-100）：非生产与生产是两个实例、两个数据库，应用各自登记，ID 不同（例如 checkout-api 在生产是 `f79b3ba7…`，在非生产是 `0a5cb011…`），只有应用 key（`applications.key`，唯一）相同。Runbook、告警规则、Agent 的清单从 schema 3 起带 `application_key`；生产导入时按 key 找到未归档、已登记 prod 环境的应用，目标对象、`agents.change` 等权限检查和导入审计都针对这个生产应用，来源的 `application_id` 只作为证据写进审计的 `source_application_id`。找不到时返回 `422 application_not_registered`。PA-100 之前导出的 schema 2 清单没有 key，仍可读取和核验，但导入返回 `422 promotion_reexport_required`：在非生产重新导出同一对象即可，新导出按 `export_format` 2 另建台账、分支和 MR，不会重放旧记录（旧 MR 留作历史）。生产列表里，GitLab 刚合并、尚未算出 `changes_count` 的 MR 单独标为 `status: verifying`（`kind: unknown`，不读取其内容），其余照常列出；此时导入它返回 `503 merge_request_not_ready`，稍后重试即可。其他核验失败仍让整份列表失败。

模板：V3 保存模板时在首个节点记录 `private_template_v1`（`steps`、`content`、`runbooks`、`source_services`）。导出不把它写进清单：`source_services` 是本环境的服务，生产实例用模板建画布时按当时选择的服务重新核权；`steps`、`content` 只记录保存时的选择，真正保留的内容在节点里，因此只有节点仍为空白结构时才可导出；关联 Runbook 时整体拒绝，不静默丢弃。告警规则：按 D32 以绑定的 `application_label`（如 `checkout-api`）匹配规则，`prom-` ID 与规则列表一致（由应用 ID 计算）；`summary`、`description` 等注解不随提升，导出快照的注解为空，生产导入的定义同样不带注解；表达式允许花括号内标签匹配的带引号值（如 `{application="checkout-api"}`、`code=~"5.."`），其他字符串字面量仍拒绝。生产导入时不知道来源实例的 D32 标签值，`application` 标签只要求是合法对象键。

`GET /config-sync` 两端最多列 100 条。非生产只列当前操作者在有权环境导出的签名记录，远端查询后重核当前对象/团队或应用权限与来源版本；生产要求当前人类平台管理员及全局 `workspace.manage`，逐项验证已合并 MR 的固定提交、唯一预期文件、签名清单、导入台账、目标记录和成功审计，远端读取后再核授权与当前会话。四类均可在目标及证据相符时显示 `imported`；`merged` 只证明当前已验证的来源状态。本读取不能证明**合并当时**的审批/保护策略。不可验证的来源返回 503，不给部分清单；超过上限返回 413。冻结页面只在 `pending` 时显示导入动作，而生产读取返回 `merged`，须另行对齐。

`POST /config-sync/{id}/import` 只接受 `mr-<正整数 IID>`，要求当前人类平台管理员和全局 `workspace.manage`，远端读取后复核当前会话，并在写入事务内再次核权。服务端核对已合并 MR、固定提交与唯一预期文件、来源签名、类型白名单、独立审批及当前目标分支保护。Runbook 导入为生产应用的可审核草稿，正文为空；告警导入为 `not_applied` 的生产配置记录，不应用到 Prometheus；Agent 导入目录版本，新 Agent 默认暂停、L0，已有 Agent 不改变发布状态。目标、导入台账和成功审计在同一事务提交；重复相同导入收敛，目标已变化返回 409。当前 API 查询无法证明**合并当时**的审批/保护策略；真实配置仓库上线前仍需保留治理配置和审查证据。不可验证的来源返回 503，不会导入部分配置。

数据库迁移依次使用 `120_v3_config_promotion.sql`、`121_v3_config_export_attempt_fence.sql`、`124_v3_config_sync_read_kinds.sql`、`125_v3_config_sync_import_targets.sql` 和 `179_v3_config_export_format.sql`（PA-100，导出台账加 `export_format`）。前三项与四类读取流程此前在一次性隔离 PostgreSQL、替身 GitLab 及定向 HTTP 测试中验证；另在本机独立私有 GitLab CE 测试项目验证适配器的 MR 创建、重复导出对账、冲突、另一账号审批合并、保护规则与合并文件读取，及只读令牌写入被拒绝。该测试项目已归档，短期令牌已撤销。CE 的 `/approvals` 响应可以省略冗余 MR 标识字段；若返回却与已验证 MR 不符仍拒绝。后三类写入和迁移 125 尚无本轮运行验证。

新增独立来源与生产 schema 的 HTTP 往返验证：模板导出后生产才可导入，重复导入只有一条台账和成功审计，新 PG 连接能读回导入模板；预占目标或导入台账唯一冲突返回 409，事务不留下目标或成功审计。正式迁移用例在两个隔离 schema 分别运行 `Store.Migrate`，但仓库仍为内存替身，并人为设置已合并状态。真实 GitLab CE 验证使用另一组定向夹具，两组证据未构成实际部署的端到端验收。另一账号审批不证明组织独立，事后 GitLab API 不证明合并当时的保护策略；当前生产信任配置也尚无按来源环境 ID 分别限定的允许列表。

尚未对现有业务库执行迁移、将服务端生产导入连到实际配置仓库或验收前端页面。`GET /config-sync`、`POST /promotions`、`POST /config-sync/{id}/import` 的契约以本领域说明和 `scripts/v3-openapi-config-sync.py` 为本次修改源；主页面契约和生成的 OpenAPI 由集成会话同步后才算一致。
