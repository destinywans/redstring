# 2026-09-30 后端测试覆盖与跳过项分类

本记录对应 WAN-138 / WAN-136。只读核对 SQLite、Go JSON、正式接口 JSONL 与实际测试源码；不修改历史运行结果。跳过、环境阻塞和外部待办均不计通过。

## 2026-10-01 当前生成器容量验证

原50RPS诊断 `a779ed68-22cc-46fe-87a4-a3df912e8ef3` 实际5通过/3失败/1首页性能阻塞；自有k6容器真实OOMKilled/ExitCode137，21.318秒无报告，后端健康。首红与原正式1a缺失退出状态分别保留，不倒推旧原因。证据 `fb5caaf1-f6f1-41db-a6d0-480377786977`。

生成器仅候选1GiB→4GiB，其他资源、原负载和门槛保持；65离线回归通过，不证明实际容量足够。暂停专用空闲GitLab而保留容器/数据、其他项目不变后，VM实际可用8,182,751,232B满足6,442,450,944B预留，下一步新的原50RPS复验。数据库代际操作ace389de已完成，新canonical11973905为基础档，三份历史库保留；PID仍需nonce/HMAC现查。完整性能未通过、24h未开始、整体未收口。完整决策/证据见CP255。

## 基线状态

| 运行 | 提交 / 开始时源码树 | 结果 | 验收限制 |
| --- | --- | --- | --- |
| `e3f2797b-1805-4fdc-be65-a39a08d38987` | `e3f0e4418d39e94cebfc5845f7d2c5fa9df361c6` / `0f8895923f2968311f08bf863284e3ac6c7972996becb2c35d29bcf1eff40219` | {'skipped': 60, 'passed': 1725, 'failed': 15, 'blocked': 1} | `failed`；source-audit：运行期间源码发生变化，冻结后需重跑 |
| `641ab5d2-7040-47ee-a977-edce3e000e4f` | `93cc4deed3e07bd253c9c947b5f4c6641056ce59` / `d1d90bacb286261a66e19ac628dd83703ae3ebed5393cf2fb53958f5bc920672` | {'skipped': 70, 'passed': 1747, 'blocked': 1} | `blocked`；source-audit：运行期间源码发生变化，冻结后需重跑 |

旧基线 60 skips = 18 无测试包 + 21 专项 PG 门控 + 7 正式入口/Token 门控 + 12 本地真实组件入口 + 2 付费模型外部待办。当前基线保留全部旧 60，新增 10 项专用环境门控，详见末节。

## 旧 60 项：精确用例与原始原因

以下包名去掉共同前缀 `github.com/wans/aiops-canvas/`。原因引用保留原日志中的文件与行号，对应该次基线，不代表后续编辑后的行号。

### 无测试包

| 包 | 用例 | 原始 skip 原因 |
| --- | --- | --- |
| `demo/labcli` | `包无可执行测试` | `[no test files]` |
| `demo/service` | `包无可执行测试` | `[no test files]` |
| `demo/telemetry/collector` | `包无可执行测试` | `[no test files]` |
| `demo/telemetry/verify` | `包无可执行测试` | `[no test files]` |
| `internal/apicontract` | `包无可执行测试` | `[no test files]` |
| `internal/core` | `包无可执行测试` | `[no test files]` |
| `internal/v3coverage` | `包无可执行测试` | `[no test files]` |
| `internal/v3executions` | `包无可执行测试` | `[no test files]` |
| `internal/v3home` | `包无可执行测试` | `[no test files]` |
| `internal/v3resilience` | `包无可执行测试` | `[no test files]` |
| `internal/v3ruleassistant` | `包无可执行测试` | `[no test files]` |
| `internal/v3rulemodel` | `包无可执行测试` | `[no test files]` |
| `internal/v3security` | `包无可执行测试` | `[no test files]` |
| `internal/v3templatecapacity` | `包无可执行测试` | `[no test files]` |
| `migrations` | `包无可执行测试` | `[no test files]` |
| `shared/search` | `包无可执行测试` | `[no test files]` |
| `tools/testlab/credentials` | `包无可执行测试` | `[no test files]` |
| `tools/testlab/load` | `包无可执行测试` | `[no test files]` |

### 专项PG门控

| 包 | 用例 | 原始 skip 原因 |
| --- | --- | --- |
| `internal/v3configsync` | `TestConfigPromotionFullFormalSchemasRoundTrip` | `service_formal_schema_test.go:68: requires explicit WAN-124 isolated PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFALockWaitRejectsExpiredSession` | `lock_expiry_test.go:64: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFALockWaitRejectsStaleFreshLogin` | `lock_expiry_test.go:74: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFALockWaitRejectsOldOTP` | `lock_expiry_test.go:81: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAFullSchemaLifecycle` | `service_pg_test.go:131: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAGuardsAndRateLimit` | `service_pg_test.go:235: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAConfirmLockoutAudited` | `service_pg_test.go:284: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAConcurrentRecoveryConsumption` | `service_pg_test.go:307: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAAuxiliaryHTTPRoutes` | `service_pg_test.go:340: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyES256RegistrationAndAssertion` | `protocol_pg_test.go:142: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyRejectsWrongOriginUVAndSignature` | `protocol_pg_test.go:191: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyDeletionRevokesCredential` | `protocol_pg_test.go:251: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyConcurrentRegistrationConsumesChallengeOnce` | `protocol_pg_test.go:293: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyRegistrationExcludesActiveCredential` | `protocol_pg_test.go:344: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyChallengeExpiresWhileWaitingForActorLock` | `protocol_pg_test.go:375: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyProposalProofUnavailableWithoutImmutableDigest` | `service_pg_test.go:115: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyRegistrationChallengeScopeAndGuards` | `service_pg_test.go:131: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyAssertionRequiresCredentialAndActiveSession` | `service_pg_test.go:192: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyFreshLoginAndChallengeExpiry` | `service_pg_test.go:208: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyRegisteredHTTPRoute` | `service_pg_test.go:243: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3passkey` | `TestPasskeyRegistrationRateLimitPreservesPendingChallenge` | `service_pg_test.go:304: requires explicit WAN-124 disposable PostgreSQL opt-in` |

### 正式入口/Token门控

| 包 | 用例 | 原始 skip 原因 |
| --- | --- | --- |
| `internal/v3tokens` | `TestIssueConcurrentCapRealPostgres` | `issue_lock_test.go:37: explicit disposable testlab opt-in required` |
| `tests/backend` | `TestFormalBackendHTTP` | `http_test.go:88: explicit disposable backend testlab opt-in required` |
| `tests/backend` | `TestMigrationFreshRepeatAndConcurrentStartup` | `migrations_test.go:58: explicit disposable backend testlab opt-in required` |
| `tests/backend` | `TestMigrationSyntheticUpgradePreservesUnknownHistoricalEvidence` | `migrations_test.go:83: explicit disposable backend testlab opt-in required` |
| `tests/backend` | `TestMigrationFailureRollsBackWholeBatch` | `migrations_test.go:139: explicit disposable backend testlab opt-in required` |
| `tests/backend` | `TestMigrationRealBackupRestore` | `migrations_test.go:166: explicit disposable backend testlab opt-in required` |
| `tests/backend` | `TestTokenIssueRealPostgres` | `token_postgres_test.go:18: explicit disposable backend testlab opt-in required` |

### 本地真实组件

| 包 | 用例 | 原始 skip 原因 |
| --- | --- | --- |
| `internal/connectors` | `TestJenkinsLiveReadOnly` | `jenkins_test.go:75: temporary Jenkins endpoint and job not configured` |
| `internal/connectors` | `TestLocalReadcheck` | `telemetry_local_readcheck_test.go:17: AIOPS_TESTLAB_TELEMETRY_CONFIG required for explicit local live telemetry` |
| `internal/deployment` | `TestLiveRegisteredDeployment` | `live_test.go:38: explicit new-lab config required` |
| `internal/delivery` | `TestLiveConditionalBranchAndSelectedJoin` | `live_branch_test.go:11: set AIOPS_DELIVERY_LIVE_LAB for the isolated aiops-test lab` |
| `internal/delivery` | `TestLiveGitLabActions` | `live_gitlab_test.go:15: set AIOPS_DELIVERY_LIVE_LAB for the isolated aiops-test lab` |
| `internal/delivery` | `TestLiveGitLabLostTriggerResponse` | `live_recovery_test.go:32: set AIOPS_DELIVERY_LIVE_LAB for the isolated aiops-test lab` |
| `internal/delivery` | `TestLiveArchivedLogRetrieval` | `live_recovery_test.go:62: set AIOPS_DELIVERY_LIVE_LAB for the isolated aiops-test lab` |
| `internal/delivery` | `TestLiveNativeImageBuild` | `live_test.go:54: set AIOPS_DELIVERY_LIVE_LAB for the isolated aiops-test lab` |
| `internal/delivery` | `TestLiveNativeBinaryBuild` | `live_test.go:54: set AIOPS_DELIVERY_LIVE_LAB for the isolated aiops-test lab` |
| `internal/inference` | `TestOllamaRealSmoke` | `runtime_test.go:158: explicit opt-in; invokes local model` |
| `internal/v3identity` | `TestOIDCKeycloakLocalSmoke` | `keycloak_smoke_test.go:32: AIOPS_OIDC_TEST_ISSUER required` |
| `internal/v3integrations` | `TestRealLocalConnectorProbes` | `real_local_test.go:36: set AIOPS_TEST_*_URL variables to smoke-check local read-only providers` |

### 付费模型外部待办

| 包 | 用例 | 原始 skip 原因 |
| --- | --- | --- |
| `internal/inference` | `TestDeepSeekRealSmoke` | `deepseek_smoke_test.go:15: opt-in paid provider check` |
| `internal/v3conversation` | `TestV3DeepSeekHTTPPGSmoke` | `deepseek_http_smoke_test.go:86: opt-in paid V3 DeepSeek HTTP/SSE/PostgreSQL smoke` |

## 已有替代证据及其边界

| 分类 / suite | 实际证据 | 证明范围与后续 |
| --- | --- | --- |
| `mfa` / `passkey` / `configsync`（21 项） | `53de40b5-8c9f-4ce3-a466-2b3b8f0cbd11` 三 suite 的上述精确 case 通过；开始源码树 `9ad8ccd69b4ff42bf9a706a5dc608ff1272aff6fec024408c3ea321ce1309b6a` | 真实专用 PG、MFA/软件 WebAuthn/配置提升；整体仍 source-audit blocked。冻结后重跑三 suite。软件认证器不关闭真实物理钥匙验收。 |
| MFA 锁等待专项 | `fde81ce1-7cee-4a1f-b233-a31eb460605c` 8 passed，无 skip/failure；源码树 `537406299456cd79417902d483dcc437a1258b72105197806f452ad34b577443` | 真实 factor 行锁与 audit INSERT 锁跨会话/OTP/新鲜登录期限；证据只对应该版本。 |
| `entry` / Token（7 项） | `.local/testlab/fix-regressions/backend-full-race.log` 中四 migration case 通过；`backend-http-green.log` 中正式 HTTP 与 Token case 通过；`token-module-race.log` 中 Token 并发上限与 lock key case 通过 | 新库重复/并发迁移、合成历史升级、失败回滚、真实 pg_dump/restore，以及双实例业务 HTTP。普通日志缺乏完整 SHA/接口矩阵关联，冻结后由 runner 执行 `entry` 并持久导入。合成旧库不代表生产存量升级，测试 schema 备份恢复不代表完整生产灾备。 |
| `components` / Keycloak | `.local/testlab/environments/testlab-3cbde3095125/keycloak-go.jsonl` 精确 `TestOIDCKeycloakLocalSmoke` pass | 独立真实 Keycloak auth code、callback、PG session/audit，不代表生产身份源。冻结后 components suite 登记。 |
| `components` / native 观测 | `native-components-race.log` 的 `TestNativeRealComponents`；`formal-native-consumers-race.log` 的 `TestFormalBackendNativeHTTP` 五次重复通过 | 真 Prometheus samples、Loki 精确日志/隔离、OTel→Tempo spans、Vault KV2。并不使 `TestRealLocalConnectorProbes`、Grafana、旧配置 `TestLocalReadcheck` 自动通过。 |
| 无测试包（18） | 仅 `[no test files]` | 不是 18 个已执行失败用例，也不能推断功能缺实现。migrations、apicontract、templatecapacity 部分有间接证据；七个 V3 业务包 `v3coverage/v3executions/v3home/v3resilience/v3ruleassistant/v3rulemodel/v3security` 仍需具体 route/逻辑证据映射，不可将其他包绿映射全部接口。 |

正式 HTTP 通过的八个业务子场景为 `AuthorizationAndSessionRevocation`、`WorkspaceBoundaryETagAcrossInstances`、`TokenAuthorizationIssueConsumeRevoke`、`CanvasPositiveAndCrossEnvironmentBoundary`、`RunbookPositiveAndCrossEnvironmentBoundary`、`ConversationPositiveAndCrossEnvironmentBoundary`、`UnconfiguredAuthorityFailureClosed`、`IncidentIdempotencyLifecyclePostmortemPositive`。这些 suite 级证据不能凭源码补造每个接口/场景的通过记录。

## 本地仍可补齐与外部待办

| 用例/组 | 本地下一步与限制 |
| --- | --- |
| `TestJenkinsLiveReadOnly` | 新建本轮自有 Jenkins、job 和至少一条真实 build；设置 `JENKINS_READCHECK_URL/JENKINS_READCHECK_JOB`。已有 httptest 仅协议 Mock；当前组件清单无 Jenkins，不能说是必须用户提供密钥。 |
| `TestLocalReadcheck` | 提供专用 `AIOPS_TESTLAB_TELEMETRY_CONFIG` 与匹配 `lab-order-api` 的合成应用日志；native 模块使用其他受控数据，相关能力已验证但旧 case 未执行。 |
| `TestLiveRegisteredDeployment` | 提供新 owner 环境的 deployment config/source/connection；旧 fixture 包含 aiops-test GitOps/VM/S3 特定目标。不得借用其他练习集群。规则 GitOps 验证不等于 VM/应用发布与丢响应恢复。 |
| 六个 `delivery/TestLive*` | 旧 fixture 严格绑定 aiops-test 路径及 GitLab/Argo Workflows/S3 端口。需迁为新 owner fixture，并真实覆盖分支 join、GitLab actions、lost-trigger-response、归档日志、原生 image/binary build；新规则 MR→Argo→Prometheus 链路不会关闭这些。资源不足须明示环境阻塞。 |
| `TestOllamaRealSmoke` | `AIOPS_REAL_OLLAMA=1`；目前 hardcode localhost:11434 与 qwen3.6:latest/bge-m3:latest。先建立自有 endpoint/模型资源，不直接调用用户既有 daemon。协议 Mock 不证明真实推理/embedding。 |
| `TestRealLocalConnectorProbes` | 显式设置 `AIOPS_TEST_{PROMETHEUS,LOKI,TEMPO,GRAFANA}_URL`；只配置部分 provider 只能证明该子集。当前 native 实际消费证明不替代精确 probe case；Grafana 当前未建。 |
| 两个 DeepSeek case | `AIOPS_REAL_DEEPSEEK=1` / `AIOPS_V3_REAL_DEEPSEEK_SMOKE=1`，用户提供真实 key 且计费边界后再执行，当前外部待办。Mock 不关闭真实模型验收。 |

飞书、Notion、Confluence、真实组织权限、付费模型、物理 WebAuthn 等外部待办不一定各有 Go skip 用例；不得把本表两项 DeepSeek 当成全部外部缺口。Eval、Agent/MCP 执行器、权威生产者等已确认缺实现以原缺口台账为准，不能从 `[no test files]` 推断或关闭。

## 407 接口库存与正式运行矩阵

库存来自 `internal/apicontract/openapi.json`，407 个 method/path，包含历史和条件式接口。源扫描只登记接口声明：所有场景初始 queued，不能证明正式 V3 注册。实际注册应核对版本与配置，不把中间件返回 401/403/404 当成 handler 注册证据。

核对时 SQLite 中 6,105 条 interfaces 记录全部为 `source`，没有真实注册证据被导入（407×15 次运行库存）。直接 Go 正式接口日志与 runner 矩阵尚未完成关联。
当前 `.local/testlab/interface-results.jsonl` 留存 214 次请求：210 passed、4 failed，只有 5 个独立 method/path。最新追加运行 `ad45d3c697a74ea7f18055c29248e537` 为 94 passed、0 failed，证明如下 12 个接口×场景组合：

| method/path | 已记录的场景 |
| --- | --- |
| POST `/api/v1/auth/login` | normal |
| GET `/api/v1/metrics/samples` | normal / authorization / boundary / failure |
| GET `/api/v1/logs` | normal / authorization / failure |
| GET `/api/v1/traces/{id}` | normal / authorization / boundary |
| GET `/api/v1/traces` | failure：上游范围不可证时诚实降级；未验证可用正常路径 |

其中四个接口有 2xx 正向注册证据；第五个只有错误/降级场景。94 次请求不是 94 个接口，5 个接口更不能代表 407 正常全绿。当前没有可据此发布的完整接口覆盖率；GET 的幂等是否适用也须按契约判断，而非机械补五种场景。
旧正式 HTTP 逐调用文件曾被测试入口截断；root 已移除截断行为。首次失败及业务回归 stdout 日志仍保存，但缺失的旧矩阵不能重建。下一冻结 `entry` 必须实际执行两次，核对第二次从原文件 offset 追加、首轮记录与失败记录仍保留，并让 runner 持久导入准确 registration/scenario 证据。
模块 Go test 能证明其实际断言的状态、RBAC、锁、事务、协议或 httptest handler；不能自动证明 cmd/aiops 生产接线、条件式注册、反向代理/TLS、跨进程恢复或真实租户授权。Mock、真实 PG、真实依赖、正式二进制 HTTP 必须分别记录。

## 当前新增 10 项跳过

| 包 | 用例 | 原始 skip 原因 |
| --- | --- | --- |
| `internal/v3mfa` | `TestMFAFactorRowWaitRechecksExpiredSession` | `lock_expiry_test.go:179: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAAuditWaitRollsBackExpiredTOTP` | `lock_expiry_test.go:195: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAAuditWaitRollsBackRecoveryWhenSessionExpires` | `lock_expiry_test.go:211: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAAuditWaitRollsBackEnrollmentWhenOTPExpires` | `lock_expiry_test.go:227: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3mfa` | `TestMFAAuditWaitRollsBackCancellationWhenFreshLoginExpires` | `lock_expiry_test.go:248: requires explicit WAN-124 disposable PostgreSQL opt-in` |
| `internal/v3webhook` | `TestWebhookInsertBindingRealPG` | `persistence_pg_test.go:105: explicit disposable testlab opt-in required` |
| `internal/v3webhook` | `TestRegisteredWebhookPersistsRealPostgres` | `persistence_pg_test.go:162: explicit disposable testlab opt-in required` |
| `internal/v3webhook` | `TestRegisteredWebhookMultiAlertRollbackAndArchivedMapping` | `persistence_pg_test.go:200: explicit disposable testlab opt-in required` |
| `tests/backend` | `TestNativeRealComponents` | `components_test.go:218: explicit disposable backend testlab opt-in required` |
| `tests/backend` | `TestFormalBackendNativeHTTP` | `components_test.go:353: explicit disposable backend testlab opt-in required` |

新增五个 MFA case 由上述 `fde81ce1` 专项验证；两个 native case 由对应真实组件/正式 HTTP 定向日志验证。三个 webhook PG case 的最新 SHA 对应专用执行记录需补核，不能仅因门控跳过或包内其他 Mock 用例通过而记为已覆盖。

## 冻结后仍必要的动作

1. 冻结最终源码后重跑 baseline + mfa/passkey/configsync + entry/components；不得将 source-audit blocked 转写为成功。
2. entry 连续两次检查追加 offset、证据来源/SHA/fixture owner，并持久更新真实接口场景，不回填历史推断。
3. 对 Jenkins、旧 delivery/deployment、Ollama、Grafana 等本地可覆盖条目明确执行/环境阻塞和下一工作包；外部真实账号保持待办。
4. 正式接口全覆盖、性能、真实进程故障以及连续 24 小时各自独立验收；本记录不能代替这些结果。

## 追加：正式入口与冻结定向结果（不覆盖原失败）

- 正式entry `a205fa0a` 216通过/10失败含父/命令重复：Header OWS/DTO source_ref/发布后cleanup FK/connector类别四类测试工具与夹具错误。首红与源码指纹保持，修复后 `b8241602-b09f-47bd-a736-bdc0c3293c92` 230通过、0失败/跳过/阻塞；开始/结束Go指纹均 `ee995059ddd7fc022c4c8a444c015bca681a909e5dc73a29c03e552d4b1aa7e6`。
- 本次实际矩阵已由Runner持久导入，**41个正常method/path、48个有实际响应**，包括25path/32method匿名边界、偏好/AI协议Mock/通知跨实例、Runbook版本/独立审核/发布/搜索/分页/对话快照、同环境跨app拒绝、告警ETag和幂等、Token发/消费/撤销及原生消费者。并非166项当前V3或407项历史库存全覆盖，也不把401视为正常handler注册。
- 冻结专项/已知风险 `f01915c1` **648通过/0失败/1跳过**。唯一跳过 `internal/v3conversation/TestV3DeepSeekHTTPPGSmoke` 为真实付费外部opt-in，保留待办和run blocked；三个Webhook PG与Token并发容量原门控已在此轮开启覆盖。
- 真实故障 `5b3913ec` **7通过/0失败/1真实付费模型外部待办**，源始末一致。TERM/KILL、PG租约恢复、SSE原始/恢复精确重放、跨实例持久会话/Token撤销已验。模拟供应商不证明实际计费推理故障。
- GitOps `1f93d97f` 真双人投票与DB边界通过，最终240秒生效读回超时仍失败。labels/assurance首红属于夹具，不改变产品限制；完整MR→独立审核→Argo→实际Prom规则尚未关闭。
- 12秒 `92792779` 接线自检5项通过，实测50正常/10慢SSE及资源数据已呈现在独立看板；它不能计为正式18分钟性能/24h。后者尚未启动。
- 正式OIDC、SMTP邀请及Vault凭据三条本地短链将在最终性能/24h前补；真实外部租户/最终邮件投递保持待办。baseline `2d58418a` 因新确认WAN-155修复将改变源码而提前停止：463通过/0失败/17跳过/2中断，保留记录，修后重跑。

## 追加：当前真实短链及接口分母校正

原entry41个正常method/path与主166契约交集为**30**；另11为辅助/历史。主135条正常路径还没有该entry证明，不等同缺实现。后续每条新短链独立登记真实HTTP矩阵，不从模块通过补推断注册。

- WAN155 `e9046eb` 后 `0bce4262` 5通过：真实软件WebAuthn独立双人投票/第一票不执行、持久receipt、GitLab MR5独立审阅合并、Argo同步、实际Prom规则生效；原1f93失败保持。物理认证器与生产治理不关闭。
- WAN156 `4d43d21` 两审计SQL类型修后 `611892f9` 整v3admin30真实PG/race、`7edb56a7` 正式HTTPS+TLS465四跨组件时序6项通过。上游已接收、本地audit提交失败、响应丢失分别有证据，最终外部投递仍待办。
- `796b27b7` 24通过：正式connector六scope credential绑定、真实Vault least-policy/deny与受控TLS上游。不是所有企业Vault权限/网络出口验收。
- `9997769d` 10通过：原TestLocalReadcheck及四provider probe使用真实本地组件，Grafana12.1.1为临时专有容器已清理。Jenkins仍在复验，原Groovy import首红属工具fixture，新的启动退出须另诊断，不无证据归为OOM。
- OIDC935f尚未通过，实际KC登录cookie_not_found确认HTTP loopback Secure cookie工具差异，决定仅临时KC改HTTPS可信CA。原512MiB OOM另为有证据环境阻塞。content a98首次8pass+SQLfixture block保持，确认setup两个角色同name违反UNIQUE(name)，业务阶段尚未开始，改精确run-owned角色name复验。
- 当前全量 baseline324a进行中；旧2bfa847pass/17skip/1interrupt与2d58463pass/17skip/2interrupt因产品修复前停止，不能作最终冻结证明。正式18分钟性能/真实连续24h仍未开始。

## 2026-09-30 最终Go指纹与独立短链补验

- Go指纹`be282465cdf1078ec20e393bdf98b091817b9f1eda7704a9d737e4e46f26ca65`，全量`324a7add-47f6-4db8-a9ce-7840c7048cdb`1815通过/0失败/75跳过，始末一致。75分类18无测试包、11旧local_live、2付费外部、44独立opt-in；原skip保留，专项证据分别关联。
- 专项/定向`55654252-6fd2-4a6e-9072-4cec58f3e3fc`664通过/0失败/1付费DeepSeek跳过，运行诚实blocked。不能把独立验证覆盖过的opt-in改写为原全量无跳过。
- 正式HTTPS OIDC18及Vault24按新cleanup/matrix重验`fa8fbd5f-12e9-4de9-8d06-ba9db2780991`42全部通过，27实际HTTP矩阵记录；企业IdP/组织策略待办。内容`e8814ea6-326b-4a5c-b73b-1571a9998e57`69全部通过/62实际HTTP矩阵记录，明确合成application-bound unpublished draft和Canvas输入；正式独立审核、版本引用、模板三保留模式、上传URL/SSRF、事件复盘正常与跨scope均由API执行。
- Jenkins`4cc8cd6d-edf4-481b-a825-e1e1834824a1`原live1通过，本轮任务真实成功并读取console/时间和结果，另受限reader请求build403；非产品认证constructor/真实交付流水线证明。三工具首红原样保留。
- 普通静默`16772598`KeyError以及`3e0709ac`seed UNIQUE(role name)均在正式HTTP前；工具错误按真实schema修复中，不能宣称已验静默能力。配置提升WAN157工具准备中。
- 用户授权并行后，WAN158独立HTTP只读补测与WAN139独立看板QA已派发可见会话；新增独立DB/18602/18603，主会话统筹修复与最终性能/24h冻结。未重复已完成全量/迁移，未改测产品前端。
- 各最新完整运行已导出HTML/JSON/Markdown至`.local/testlab/reports/<run-id>/`，运行/证据持久保留，不提交数据。正式18分钟/规模性能与真实连续24h尚未开始。

### 原生交付历史跳过的范围核对

Linear WAN89/90/91/92最后确认仍明确暂停CI/CD开发、构建测试、部署和故障验证。本轮规则审批/GitOps为明确批准工作包，已实际验收；delivery6/deployment1七个旧live与该规则链不同。它们不是外部账号阻塞，而是暂停域范围待用户后续Review，本轮暂不启动Runner/Registry/原生构建/VM发布。此决定更正此前把所有旧live视为继续执行工作的推断，保留原历史/skip和高优先级台账，不把它们计passed。Ollama本地model仍独立待本轮隔离配置/容量核对，不属于这7项暂停域。


### 2026-09-30 23:45 · 并行包首轮与源8d0a验收记录

WAN159 a00c245修143迁移漏cancel并新增153已记录数据库补迁移；3真实PG迁移红绿及独立审查完成。源8d0a正式普通静默bd46318f55全通过、entry+components05ccc193244全通过，报告保留；全量c5063e10仍运行，旧be2821815/664结果保留但不是当前冻结验收。

可见HTTP会话WAN158完成31离线守卫；根首live848ce917发现home缺oncall时503的夹具契约预期错误、5秒cleanup超时及malformed事件，留首红并返回独占两文件修正，不静默放宽断言。独立看板QA完成，两实际问题已b083243最小修并22离线/实际三报告下载复验；旧flat无结构化场景明确证据不足，实际/预期HTTP码显示，fresh连接和持久刷新恢复已验，同tab跨重启自动重连未直接验。

配置提升471d5d0e73业务/故障断言通过（真实GitLab branch201/MR201接收后丢响应、直接上游读回、安全同digest重试；签名对象跨项目200拒绝与去故障成功因果对照）。源/生产DB清理超时导致父run失败，分别保留产品业务与清理错误，不计完整通过，原工具代理继续最小修后根复验。

性能监督者由plain identity改为新nonce/HMAC校验，二进制与依赖验证工具纳入22输入冻结；18离线和实际owned实例核验通过c784eac。本轮性能/24h尚未开始，禁止以12秒自检或并行总耗时替代连续窗口。主证书至2026-10-02T08:58:11Z，启动前仍需实际窗口校验。


2026-09-30 23:54定位更正：848ce917首红精确路径为GET /me（权威oncall缺配置→503 oncall_unavailable），此前记录将http.7误认为/home；/home成功断言保留。fixture、pagination、dependency_unavailable三个工具场景名不在Runner枚举，malformed为采集协议问题；按canonical场景记录细分purpose，不扩大允许值或冒正常覆盖。原首次失败不覆写。当前冻结回归c5063e10已2479通过/0失败/79跳过，源始末8d0a一致；分类18无测试包/12本地live/3付费外部记录/46专项开关，30个skip对应同run另专项实际pass，全部原skip保留。


### 2026-10-01 00:30 · 并行策略及工具异常修复

用户再次授权睡觉期间自主选择、留记录并次日Review。三条可见测试包加根统筹构成最多4工作流：HTTP只读WAN158、看板QA WAN139（已结束）、新服务/管理写入WAN160会话01a0f31a-4262-75a2-806d-088f6c9e19f7（只offline、独占2文件、新DB/18606/07、限定12–20本地无需账号路由）。根内部模块代理处理逻辑/迁移/配置提升/GitOps，重型资源与正式性能仍串行，未量化壁钟节省百分比。追加管理包原因：历史44/166正常主路由证明仍不足，优先补现有本地能力，避免把匿名/越权断言、Mock和库存当正常验证。

配置提升55c2d57f75本地实际断言通过/0失败/1external_todo、62离线，3944284提交推送；源/生产DB精确无残留、进程exit0/PAT已撤销，GitLab项目8/9/10仅accepted延迟删除，不宣称物理回收。首471d清理超时由精确reconcile修，不重复DROP，总40秒不变。

共享identity错误留证可能跳过cleanup或覆盖原始业务异常，f902cc0最小finally/独立sink修后37身份/101依赖offline及源8d0a实际identity+Vault6c911b2f42全通过；IO故障无法写证据时仍失败/保留原异常，不冒绿色。readonly对应41offline、原live980275ad96业务通过0业务失败，但PG连接尚在导致清理阻塞，保持整体failed；精确源8d0a，继续只读定位、有界quiescence、不force连接/盲目DROP。

规则GitOps重复prepare327be9707通过/2失败/2阻塞：官方Argo安装清单覆盖repo CA，准备HTTPS启动窗口失败后半准备chain同步400。owned集群/node精确归属、无OOM/映射漂移，actual Argo日志unknownauthority与ComparisonError证明环境根因；首TLS精确URL历史无法还原仅静态首session推断，保留。工具修恢复CA顺序+验证TLS只读health/readiness+bootstrap_ready失效/精确预检，9新增/14已有offline通过，原suite d017a14b复验中。禁止insecure或绕过独立审核。

冻结全量c5063e10最终2479通过/0失败/79跳过，源始末8d0a一致。分类18无测试包、12local_live、3付费外部记录、46专项opt-in（78unique module/name，30条在同run专项另pass，原skip不改）；同源entry244补16backend gates，另有原local telemetry/Jenkins/OIDC事实。7旧CI/CD live保持暂停范围HighTODO、Ollama容量High阻塞、付费/真实租户外部待办。正式性能与86400连续窗口仍未开始。


### 2026-10-01 01:10 · 并行包验收与性能前冻结

WAN158只读1fbdd662-d5e8-464b-a0dd-5684d64b830b最终98全部通过/89实际HTTP，26主GET与1辅助GET取得正常2xx；两文件53离线通过、2a81bfb提交推送并远端确认。前848/980/79首红均保留。第三首红实际PG快照仅精确自有库autovacuum维护进程，被客户端归属守卫误判；最小核验内部字段并只读有界等待，不给后台PID发信号、不FORCE。按旧私有清理记录恢复后三个自有库/角色均确认消失；b732dd59四项和f95aaa9e两项通过。恢复临时manifest重复用例键22e8首次工具红保持，拆独立命令复验。

WAN160管理包由独立会话完成44离线，根正式fdd28078-b649-45b0-b103-03a9a4bdc8d0 130全部通过/113实际HTTP；17目标中15正常2xx，个人资料GET/PUT仅缺权威503，不计正常。真实双API同ETag/workspace与role revision冲突各仅一赢家，读回胜者/版本和精确事务审计；关注/取消幂等、用户偏好隔离、机器角色不能获取审批权限、session撤销跨实例失效。未新增产品Go修改。可见会话已要求停止编辑/负载，源始末8d0a一致，工具校验和在独占执行后保存，不能冒称运行前冻结。

最新GitOps d017a14b-cc79-426a-a28f-8effa308fd4b 11全过、8353fdc已推送；配置提升55c2d57f75本地通过/1外部待办，identity/Vault6c911b2f42全过，普通静默bd46318f55全过，entry05ccc193244全过。全量c5063e10 2479pass/0fail/79skip的分类与关联专项证据保持；外部/暂停CI/CD/本地Ollama容量/WAN152聚合均不绿色。

最终静态b082ef71-27ce-441f-8647-c73ea3dbc0d5六命令全通过：382工具、18持续/性能、14GitOps离线，407契约、readiness与go vet。doctor所有隔离/端口/组件通过；owned base supervisor59598/API59599/59600未重启；证书实际2026-10-02T08:58:11Z且checkend90000通过。正式18分钟基础/规模性能、连续86400窗口仍未开始，接下来先性能再新trial与持续窗口。产品前端/默认分支/产品部署均不触碰。

2026-10-01 01:10：管理包独立XHigh只读审查未发现阻断路径；原130 live已经验证并发、审计及精确清理。两文件提交389e994，push继续确认。正式性能 run 2e7d6d01-ff84-4524-ae43-97bb643ae7ac 已启动，基础后规模串行，不能预告通过；连续24小时尚未启动。


### 2026-10-01 01:20 · CPU/RSS采样边界与后台接续

性能首run2e7d6d01在真实预热后发现沙箱禁止ps（exit127 operation not permitted），CPU/RSS明确缺失。只读提升权限核对自有59599/59600指标可采，按SQLite及当前command双核验仅向runner76384发TERM，原run准确interrupted且三格式报告保留。未改源码/负载/阈值，base API未重启；不将该次窗口算通过。

完整重run06a848d7-6821-463f-96bd-1ee2639ea6f8于2026-09-30T17:15:03Z启动，require_escalated仅为自有进程采样；最新120秒预热实际完成，0错误、P95约238ms/P99约274ms，不替代基础25RPS验收。CPU/RSS/FD/PG实际有样本，goroutine/heap仍标缺失。登记runner77575，仅为它配置临时caffeinate -i -w，退出自动结束，不改系统电源配置/保持屏幕。

已创建本会话heartbeat aiops-24（每5分钟、状态不变静默），先检查既有run和精确冻结事实，收只读166接口剩余审计，修必要失败，再按原scale/86400新trial持续；不得重复启动、改冻结源码/配置/重启API/并行重型负载或恢复原暂停CI/CD。真实24h还未开始；结束后报告、Linear、commit/push且暂停本接续，不扩大长期监控。用户睡前自主选择授权与原24h计划为接续依据。


### 2026-10-01 01:25 · 剩余接口本地补测选择

独立只读审计17:14:49UTC快照：主166条，当前源8d0a有78条实际结构化HTTP，71条正常passed2xx，其中67在source始末一致且终态passed运行；另外4条正证据来自promotion55c仅external_todo使父runblocked，core与cleanuppassed不能抹去其本地正证据。跨版本83条有实际结构化HTTP，另83条从未有这类记录，不能从407库存推出正常覆盖。当前7条只有负例/降级（me、GET/PUT me/profile、security、traces、knowledge/search、timeline），具体属于配置/夹具/能力而不是自动视为产品Bug。

选择在性能结束后串行补现有本地低依赖知识keyword非空搜索、审计export→ownerCSV下载、people peek；内部代理仅offline独占新discovery_chain.py与对应测试，复用readonly生命周期工具、不改Go/perf冻结输入/不启动资源。另旧content未被159影响的证据仍可复用，但再原包串行实测可刷新当前8d来源。当前性能不并行live，24h前先收结果/必要修复/受影响回归。新增工作复用WAN158，不为聊天或单文件新建Linear项。该选择为助手依用户自主授权决定，供事后review；覆盖审计不是源缺陷证明或新的架构需求。


### 2026-10-01 01:30 · 性能真实失败，暂停持续准入

性能06a848d7已终态failed，14通过/4失败/8blocked（含父项及两档重复聚合blocked，非4个产品Bug）。基础warmup及rps10真实满窗零错误；rps25约105s冻结就绪检查Blocked: two ready registered child PIDs required，API健康两秒context deadline exceeded；随后规模warmup13s也同样失败。停止负载后同59598/59599/59600存活且ready true，未重启。PG当时25conn、13–16active、0锁等待，实际日志大量conversation COUNT UNION取消，checkpoint同时存在；现有证据不能断言唯一根因。

已派内部只读专业诊断区分产品查询/索引、PG环境与工具门槛，不改health时限、数据量、权限或性能阈值掩盖；所有stage原始k6/resources/日志和三格式报告留存。scale已真实seed1000/100000/500000，若需要重验基础档必须新的自有数据环境，不悄悄删除记录或把scale当base。关键性能可靠性失败未解释之前不启动24h。后台aiops-24负责继续收结论/修复原例与相关回归/重新冻结，其旧'正在性能'快照由本条终态取代。


### 2026-10-01 01:40 · WAN161最小索引修复授权

新增High WAN161（会话列表关联告警重复全扫），已记录事实与边界。当前规模受限真实EXPLAIN默认JIT on COUNT 1448.855ms/240 JITfunctions/cost2,006,460；16调查每次linked扫描100005行，约160万行/80k buffers。仅session-local jit off诊断约267ms，其中16×16.52ms linked占约264ms；不关闭JIT当修复，不断言它是唯一25RPS根因。

决定最小composite covering索引ops_incidents(investigation_id,application_id,environment_id) INCLUDE(id)，置Operations.Service.Migrate表建立后，复用已有tx/advisory_xact_lock752018996。不能塞全局154，因为ops表由主store之后Operations创建，会漏fresh初始化。内部代理独占该Migrate片段+新的有意义PG计划/语义/迁移回归文件；只在新自有fixture红绿，原scale首红APP不先加index。root负责审查、二进制重建/同源回归、fresh基础数据环境与原性能两档。预计计划改善尚未验证不能宣绿，DDL部署大表锁/WAL仍需独立限定验证。

Discovery离线包ready并停止编辑：新238+226行复用readonly深工具，14offline+语法通过；9操作用正式blank Runbook创建→提交→独立发布生成真实published source（SQL不伪发布），非空keyword/draft排除、people精确展示名、audit原生记录和export+ownerCSV下载/跨用户环境撤权。它没有live证据，待WAN161/source重新冻结后串行验证；不从离线镜像协议冒检索真实验收。


### 2026-10-01 02:00 · WAN161修复与新源回归

c14c2593b71b3466edf5e2c30ad30d32b47bc731已提交推送并远端确认，源774223bb3e48b34b1f2c4cb0358f2de9747088594e1aba9fb1934120373eafe2。仅Operations Migrate一覆盖索引+两有意义测试。完整两模块race135PASS行/0FAIL/1paidSKIP；根query-plan3f1ef462两项全过，source始末774一致且三报告导出。独立新schema真实1000app/100kalerts生产COUNT cost4276153→17900.8/defaultJIT不再触发、1561ms→0.975ms诊断；0/1/2与跨app/env、fresh/旧表8并发迁移语义均验。单app高偏斜对照仍JIT和普通index建时写锁已记录，不假装已解决全性能形态。

新source全量+专项8ad097ae-5d4f-4e40-a03f-e0dad03835e7正在运行（baseline/mfa/passkey/configsync/regressions，新增index optin明确开启）；旧8d2479证明保留但不能冒新冻结。当前正式base仍旧8d二进制，不能用它跑new774 Discovery/entry/perf；先无活动测试时精确archive旧APP→新空canonical数据库→重新runtime初始化/正式binary重建。原scaleAPP未变且不得删seed降低档位。

profile工具独占两新文件仅offline准备。自主决定唯一fresh staging创建→run COMMENT→extensions→rename canonical；未tagged CREATE-loss仍blocked，WAL意图与唯一name不单独构成归属证据。PG没有run label、原environment.run_id为wan-138-20260930，不伪造不存在的保证；CLI操作UUID/环境摘要/containerID/owner/image/volumes/原DBOID作为绑定。base未有PGAPPNAME，采取fresh HMAC二API+lsof当前15461 hostclient归属和停后零APP连接/原子rename，绝不PGterminate/FORCE。所有partial states保守，root实际apply仍未发生。


### 2026-10-01 02:35 · 新源冻结回归及归档审查

2db2a1b63b5cd68f782b18c6d460907b165f69eb测试补包已推送确认。Go始末774一致；完整8ad097ae 2482通过/0失败/79跳过，按18无测试包、12旧local-live、3付费记录、46专项开关分类，30跳过名在同run专项另有实测通过；保持原跳过不改绿。static56c03688六项通过，417工具/18性能/14GitOps/407源码契约/readiness/Go vet。三个报告和skip分类保存在私有目录，不能视为全166接口/24h通过。

归档工具profile已离线21通过，实际只读plan原APP OID16388/归档及staging不存在，操作UUID eeb805fd-25d9-4651-8ee3-fd399359df15；未实际停机/DDL。独立审查确证DSN query覆盖host等与40s预算未覆盖共享stop两处工具Bug，先两文件最小修与反例/有界停机回归，修后定向独审再apply。实际五DSN严格sslmode=disable并无目标漂移，首性能数据库仍原样。不能把工具问题记为产品Bug；未知staging拒绝接管、旧OID/tag、全失败证据继续保留。

助手依睡前授权自主决定，待用户事后Review；WAN138主线与WAN158补覆盖记录复用。24h仍未启动，当前正式API还是旧8d binary；fresh APP后重建774正式binary及runtime/原本地链，再原基础/规模18分钟负载。聚合WAN152、暂停原生CI/CD、容量Ollama和真实外部/缺能力继续高待办，整体不收口。


### 2026-10-01 03:00 · 归档安全修复与新正式实例

profile两guard修后30offline通过并经XHigh定向复核；修前24测试/57子失败保留。新static d68b5efa 六命令全过（426工具/18perf/14GitOps/407契约/readiness/vet），报告导出。实际归档operation eeb805fd-25d9-4651-8ee3-fd399359df15 7.303秒成功，旧APP OID16388和1018应用/100005告警/501326消息/16调查计数原样保留，新canonical OID9270802起始无业务表/扩展正确。未知资源不接管、无DROP/FORCE/backend termination；实际同UUID完成重放不再归档。结果登记脚本自身TypeError导致c11803a2 failed保留，修正登记后582de809三真实归档验收passed，与产品Bug分开。

已实际重建新774正式非racebinary adfbd085de53d6edcf1741e1f3dfc312cf8030e9a5b24b237198b10685c2cb17和runtime338e1478，新监督者88865/双API88866、88867 fresh HMAC核验ready；旧59598等登记快照失效。fresh DB迁移/native runtime/binding已初始化，soak freshID模板hooks保留，无复用旧trial ID。162f0b33正在entry/components/discovery/content串行实际验收，性能复验及24h仍待；不再重复归档/提前重启。旧06a性能failed、2e interrupted、全部首红与私有证据留存。


### 2026-10-01 03:07 · 新正式本地链结果与性能准入

162f0b33新774始末同源：277passed/3failed/1external_todo；其中entry/components244及Discovery33本地断言/完成/清理全部通过，实际47个normal2xx method/path含aux、非主166全部。非空keyword检索、people展示名、原生审核审计和受限CSV成功；企业身份/权威目录值班/向量provider外部待办显式保留。content第一登录401源于手动fresh准备遗漏原perf.prepare(base)，两合成用户实际不存在（OID9270802/0user/1admin），不是产品auth错误。补原prepare步骤精确100/5000/10000/20seed，不改断言/产品；原content b3db6372全部69passed，原失败/三报告保留。

最终doctor53与fresh HMAC双实例/774非racebinary、90000秒证书余窗核验通过，当前无其他active test。下步提交推送profile与记录后启动原base+scale真实18分钟阶段。base现inbox/groups200可测，home503blocked，scale以actualpreflight计，不沿用旧三路径都blocked快照。原失败数据库保持原OID与数据、不得第二UUID归档/删seed。性能/24h仍未完成，goroutine/heap不可采仍明确缺失，整体原51/166不收口。

### 2026-10-01 03:35 · 新基础档25RPS认领超时

be78e046-38f9-4abc-a045-45cc2ff16c16源774/bfb71212正式性能已中断，保留6passed/1failed/1blocked。预热120秒及10RPS完整300秒零错误；25RPS完整300.687秒7500请求中285失败（3.8%），全部是300次同用户幂等认领中的285次15秒超时。整体P95 908.619ms/P99 15001.204ms，聚合P95 919.578ms/P99 1025.102ms。PG精确窗口285次statement cancel均在认领首个incident FOR UPDATE查询；最终15锁等待。首红三格式报告、原k6/resources/PG日志与owner核验停负载证据完整保留。只停止本runner90360，正式双API未重启，规模档未seed/执行，24h尚未开始。

当前正在受限定位持锁来源。inbox/groups批量5000行SHARE为主要嫌疑，未确认实际持有者前不将猜测宣告根因；不通过放宽超时、删除有界写或仅优化脚本幂等掩盖首次写问题。稳定复现后最小修复、原场景及相关并发/权限回归，再原两档性能。旧归档数据库不变且不再次归档。助手依睡前授权自主停止更高负载并记录决定，继续修复，无产品前端/部署/合并。

### 2026-10-01 · WAN-162 受控复现与最小修复

自有隔离schema的SHARE接力实证首次认领与同用户重试均等待至1502ms测试截止；停止读后两者完成，原APP停负载后无残留锁或事务。KEY SHARE允许scope漂移、简单REPEATABLE READ在晚锁授权表后仍读撤销前grant，故未采用这两个替代方案。修复改为捕获实体SELECT的MVCC投影，并保持READ COMMITTED下当前app/env/team/user/session权限锁与末尾真实时钟到期检查；通知scope不匹配仍失败关闭、policy调用后仍完整重查成员与权限。Incidents两次实体读取之间的link TABLE SHARE保留，避免并发创建关联事件造成重复。数量上限、写锁、API结构和权限边界不变，未混入分页或N+1重构。

四helper源码与契约描述已提交推送并精确远端回读：`8d3bcf99b90c2b657af6037ec313843ab7a9de6e`。最终Go输入指纹 `15125d6ecd371864b894edfd91b7ca4944bf69dc305359d0e385a879f8cc090a` 始末一致，完整v3events真实PG/race127.732秒、48 PASS（21顶层/27子测试）、0失败/跳过。新增17业务场景覆盖三列表首次/幂等认领、旧scope快照、通知不匹配、等待期间撤权和自然到期、policy两阶段重查、人工事件status CAS及唯一event、两种创建提交顺序的去重。创建先完成再读取为顺序证据；列表先持link锁再创建是实际并发等待，不能声称两个方向都实际锁竞争。测试脚本最初误写status首次200，依据真实201契约修正并断言幂等重放200，fixture首红保留；未修改产品成功码或放宽断言。独立XHigh读取原始日志、metadata及diff，无阻断。相关纯鉴权29 PASS为c8版本中未改变实现的复用证明，最终全量将另取1512证据。

私有原red/green及 `.local/testlab/wan162-three-lists-evidence-summary.json` 保留。GroupPolicy外部响应使用协议Mock，数据库、路由handler、权限、真实时钟、认领和状态事务为真实本地；不能替代真实组织或正式V3黑盒验收。模块green不能关闭原25RPS失败。

正式双实例已按新源码重建并fresh nonce/HMAC核验：非racebinary `cb7f3d80a307256512a8e5ecd41a3ed95f2242b726816e573c2fa4a0668b4c60`，runtime `338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d`。完整静态/PG/race/专项回归 `862b1c94-dc3c-4932-8403-ba6527704831` 运行中；随后串行正式接口和原两档完整性能复测。旧归档操作不重做，原性能失败不覆盖，24小时尚未开始。WAN-162保持In Progress，原51项及全166接口尚未收口。


### 2026-10-01 · 最终完整回归与正式链复验

862b1c94-dc3c-4932-8403-ba6527704831最终2510passed/0failed/79skipped，源码始末15125d6e一致，終态blocked保持；开始Git SHA960c18e、实际Go工作树1512随后提交8d3bcf9，不能将后来Git提交冒充开始时SHA。79逐条分类：18无测试包、46专项opt-in、4本地live、7暂停nativeCI/CD、1Ollama容量、3付费外部，0未解释。30原skip在同run专项另pass，原状态不覆写。static6、baseline1839P/78S、MFA28P、Passkey25P、configsync133P、regressions479P/1S。通过数量包含Go顶层与子测试及工具记录，不是独立业务场景数量。

04c97c68-c676-4d1d-8388-643ff4cef5e3新正式链20:55:40Z通过，1512始末同源，323pass/0fail/skip：entry232、components11、content69、local-live10、Jenkins1。原16 backend+4 local-live门控均补当前源证据；OIDC通过组件调用原named Go test并断言pass，按formal_test映射，其余19有同名记录。原skip与blocked终态不改。当前50个normal2xx method/path含aux，不是主166完整覆盖。临时Grafana/Jenkins按owner/run清理；唯一已完成Jenkins合成job读回区别于7暂停原生CI/CD，未启动SCM/远程agent/镜像构建或部署。

三格式报告导出；final-862-skip-classification及followup-04c-gate-coverage私有0600，分别按5e0504e1/3d57b9cf/5dc4e7b3证据ID登记于862报告。doctor53/53、零活动run、fresh nonce/HMAC、实际1512非racebinarycb7/runtime338以及证书90000秒余窗均通过。助手自主决定补齐4旧本地live的同源证据后再压测，未扩张暂停域。原两档18分钟性能复验及连续24h尚未开始，WAN162仍InProgress，原51/主166未收口；原性能首红、归档库和所有外部/暂停/缺能力待办保留。


### 2026-10-01 · 原认领超时消退，数据库延迟仍失败

8ac896b5-70c4-43c8-a934-309ce357e31d新1512源性能21:14:05Z准确interrupted：12pass/3failed/5blocked，父/子记录非3个独立产品Bug。基础25RPS完整300.794秒7500请求0非预期错误、0丢失iteration；300次有界认领错误0，P9560.772/P99122.116ms，原SHARE饥饿未复现，PG实际samples0Lock等待。聚合P95882.335/P991142.778通过；database_latency P95676.033ms超过原500ms，P99861.518ms通过。原stage仍failed，不能因认领恢复或全局P99改善改绿。

base50约23.184秒Blocked，具体根因待核，不能先称Health故障。Runner自动接scale command，已精确seed1000/100000/500000后warmup约51秒；root fresh HMAC/SQLite/实际命令验证后只TERM runner4118，API未重启。当前APP9270802已经scale，不是base；旧eeb归档OID16388完整保留且操作完成，不重复旧UUID。若新修后确需base，须保留本次scale证据库另建fresh，不能删seed或假称基础档；当前根因未明不先操作。

三格式报告、wan162-postfix-performance-first-red及performance-stop-8ac保存，诊断摘要按28bccd29-b9ba-489e-8506-f3ec3e663317登记。独立只读高阶代理核traffic分类/具体路由/PG资源日志及bounded EXPLAIN，尚未判定产品/工具/环境根因；不放门槛/超时/权限、不删有界写/替换真实组件。24小时尚未启动，所有旧首红与新源完整/正式证明保留，整体验收仍未收口。


### 2026-10-01 WAN163 新源接口与工具闭环（CP241）

WAN163生产修复3139fc8已推送；当前Go输入bee47136，实际非racebinarycf9ab09f、runtime338e1478。完整78/auth29回归仍按071fab7及生产未再变的边界，最终NoRows测试单独bee定向。性能与连续24h尚待复验，原3d/8ac失败保持。

正式8cd312pass，readonly/discovery/management151bae239pass+1external_todo，core原8e69pass+1采集审计failed后最小phase命名修，原链0b62正式71pass/0fail/skip、61HTTP、actualclean DB/role absent。工具canonical/export scope/重复ready id三首次失败证据及定向离线回归均保留，XHigh二审放行。原生Webhooks/人工case/roles/session/audit来自HTTP；SQL只是身份输入和读回，不发送实际通知。

当前严格normal2xx主并集58/166，aux18分开，排除GET/home只有registration。空200/只读合成audit/Mock不关闭非空业务或权威验收。私有coverage证据39765127及每run三格式报告可在只读看板访问；core CSV owner下载未验证，discovery CSV另有本地真实链。剩余108路由正常证明、缺生产执行器/外部和暂停域仍需追踪；整体未收口。


### CP245：原性能分类纠正与第二授权修复边界

GET /alert-groups 和 GET /home 的正式调用链仅PG及本地计算，不能使用依赖聚合2s/5s门槛。工具保持请求、4%写、15s超时、速率/时长及原500/2000目标，分别约束可测Inbox/Groups/Home；exact限额503仍blocked。全部实际routes为DB时external aggregate无指标/无验收证明。32项离线实际JS/工具回归及独立审查通过，未新真实k6。

旧源bee的b9诊断在原25RPS300.416s/7501/零错误丢失下Inbox510.716、Groups511.526ms；be0同源稳定复验300.412s/7501/零错误丢失，Inbox541.126、Groups555.076ms均未达到原纯DB500ms。追加独立失败case后终态分别11P2F1B、9P2F1B；原mixed/aggregate通过attempt保留，报告已重新导出。

第二限定list/view单当前权限statement候选源码6ad893bb已通过真实PG定向race52P0F0S，包括新callback上的guard30动态边界；只复用held锁事实、普通授权/写/Claim/Team原路径保持，policy命令200→100仍不证明HTTP收益。完整e635回归和独立Go复审正在执行，正式API仍旧binary，完整性能及连续24h未开始。主接口旧bee严格normal58/166+aux18不能冒新6ad或完整业务/权限验收。外部/缺实现/暂停域及本地余项继续显示待办。


### CP247：第二修复相关完整回归通过

root首临时manifest漏显式DSN引用使e635成为blocked61pass/59skip，原manifest/skip/证据13623903保留，未冒完整PG通过。加prepared专属DSN引用后新aa6a2001于00:15:37–00:21:10Z真正执行完整events/rbac race和相关store/httpapi auth：135pass/0fail/skip，源码6ad893bb始末相同。定向52及最终源码经独立复审无阻断。正式runtime仍旧binary，新的HTTP及三个纯DB列表原k6门槛、完整性能/24h未执行，不能用回归或SQL往返数量声称性能修复。

## CP249：实际新源原负载和Canvas功能证据

修复a94f78f、Go6ad893bb及实际非racebfeb9b98：entry/core303、完整PG/race135、定向52后，原三阶段64cd2def于00:38:19→00:51:52Z完成11P1首页blocked。25真实300.392s/7501/0error/drop，Inbox1500次P95493.445/P99778.760ms，Groups750次474.781/713.562；原纯DB500/2000自动门槛、SSE50/10/60、资源恢复及一致性通过。证据d6ef6fb0-0ce7-4a97-8f16-9e518cba70c2；不能代完整两档18分钟或24h，旧失败保持。

Canvas4a6fd099于00:53:17→00:53:30Z完成34通过/1缺实现。27真实HTTP含19正常/8边界授权幂等，10固定目标主路由及4支持主路由正常消费通过；证据d23a1d7c-e1d4-49d9-a5b1-6f98c93a7bd6。SQL仅身份/最小grant及明示合成模板定义配置，运行graph/message/writeback/SSE由正式HTTP产生；模型为协议Mock。权威模板生产者High缺实现保持blocked；无真实推理/生产模板/大历史恢复/full166承诺。临时UUID数据库/role、独立双API/Mock清理及端口关闭已实际核验。工具取消/Mock未开始服务的cleanup首红→15offline→真实链证据独立保存。

## CP252：原生投影补验及正式性能中断

Go6ad893bb、非racebfeb9b98/runtime338e1478保持。正式performance1a998623于01:02:12→01:20:52Z中断14P2F5B；base25实际300.469s/7500/零错误丢失，Inbox460.267/657.090ms、Groups450.473/623.800达到原500/2000门槛。base50仅20.874秒k6退出无summary，原自动删除容器使ExitCode/OOM证据缺失，不能断言OOM或产品故障。当前APP11131999已scale，旧证据库保留；完整两档未通过、连续24h未开始，collector取证/取消45秒总预算及原异常保留仍在修复，未清关不再负载。

新包第一次c53be887失败24P4F是人工Case没有权威开始时间，测试误要求duration非空；第二次7c8a8e0f失败32P4F是本包public_host为空，测试误要求configured_unverified。生产nullable/未知MTTR及endpoint_host_unconfigured均正确。原失败、raw、三格式报告保持；测试严格改为准确未知状态、缺失公开host和正确method/path，原响应红绿重放与33离线通过，不修改产品伪造权威数据或假公网地址。

最终9d1f67ab-fd09-4edc-a744-c72eac6a712c于01:59:56→02:00:09Z完成49P0F0skip/block，36次实际HTTP，五目标 GET/postmortems、GET/ingest/push、GET/webhooks、GET/PUT/service-mappings 正常链通过。原生告警、人工事件CAS结案、独立peer发布、自审拒绝、签名Push接收与重复、非synthetic事件/回执/审计及mapping跨实例精确CAS读回均实测。数据库、角色和本次双API清理核验，主进程/源码未改；证据bc6690f7-4489-4236-931e-01605d44a091、三格式报告及raw均可追踪。

动作仅证明数量，不证明内容/完成率；人工事件持续时间与MTTR仍未知；Push没有公开地址，不验公网可达性；本地真实Prom注册及mapping配置不证明指标、完整服务健康/历史。SQL仅身份/最小授权/明示配置和读回，无业务或审计写、模型调用和外部通知。原工具readiness及取消缺陷已31离线/XHigh回归，后来两项预期误报的严格修由root对照生产调用链和原raw复核。

当前源三组实际证据0e4b303、4a6(34P1缺producer)、9d1f49严格normal实际2xx并集57/166主路由+13辅助，证据d20d47ec-405e-4260-ac80-0bb28ce07ca6；不计库存、negative-only或旧源码证据，不承诺全部参数/权限/并发/全Schema。剩余本地、外部真实接入、权威缺实现、WAN152限额、暂停7nativeCI/CD和Ollama容量继续待办，整体验收未收口。

## CP258 · 2026-10-01 · 测试工具健康失败传播修复

原4g诊断ce56b388只25.92秒，真实签名ready=false但进程存活；首红及原证据989d73d5保留，容量和PG退化根因未验。限定50/300、100/60测量可持续采样，但健康失败锁存，完整测量不能变通过。独立审查再识别默认25/soak的metrics catch吞身份失败，同类错误反向闭环：保留原strict validator，移动两次identity到资源catch之外，仅资源探针失败可missing。

原72/77候选均未提交或真实使用，全部红绿0600保留；最终80P0F0skip（raw5.698s，外层5.8353s），Go6ad始末不变，XHigh定向无阻断。实际metrics/identity+签名transport stub、本地Python子进程和CID清理夹具，非真实Docker/PG/API/24h。最终perf SHA22fbb6042e38d24a9543a4e12fc69673927f8b2186ea2b3f27f46734d840538a；首红k6-strict-metrics-1d2ebdb8，绿k6-strict-metrics-offline/a0b22821。

当前无active负载，源/正式API/基础11973905/三历史库/暂停自有GitLab不变。下一步新不可变原50诊断并只读取PG/k6容量证据，再原base-only五阶段/明确结果闸/scale-only。两档性能未通过、24h未开始、整体未收口；暂停域、外部、缺实现及WAN152容量边界继续待办。

## CP260–261 · 原高负载完整失败与联合CPU容量档

18a5d739原50/300于03:50:23→03:56:52Z failed7P4F1Homeblocked，Go6ad始末相同；k6实际306.735s、10904请求、4097调度丢弃、0非预期HTTPerror，exit0/OOMfalse，自己的CID清理。健康false两次锁存、SSE21窗口违反，原60秒恢复与一致性通过。证据9d982a0d与全rawa8eb及三格式报告保留。额外observer25采样实际PG2CPU2GiB、k62CPU4GiB，两份5秒cgroup差证实频繁配额节流；内存usage峰k6约1.30GiB不是RSS。只支持容量约束假设，未证明唯一根因或Go池Acquire队列。

独立只读评估认可一次明确联合4CPU容量复验。仅生成器固定CPU2→4、内存4GiB及所有原负载/门槛保持；81offline绿/原81首红5F保留，非实测PG修改或性能证明。当前PG仍2CPU，只有无active/freshHMAC/VM实际空闲/精确ownedCID-image-volume-OID-原limits/readback后才调整同一PGCPU；新recipe、freeze和run，不覆盖旧2CPU结果，不直接启动24h。完整性能未通过、24h未开始、整体未收口。

记录纠正：CP258证据f9d2因Store根参数错误落到嵌套私有台账，原保留；正确canonical证据0673fba7-509e-464c-9560-d67873632aaf已进入真实看板，关联原ce56但不修改其cases/status。观察器首份0sample OID string/int误报也保留，新私有v2仅接受同精确OID，额外取证不冒formal基线。

## CP263 · 联合4CPU原50诊断支持路径通过

8ff4c9f9于04:12:31→04:18:51Z完成10通过/0失败/1首页性能阻塞，Go6ad始末一致。原50/300真实300.832秒、14943请求、58调度丢弃、0非预期HTTPerror；health unready0、SSE窗口违反0、原60s资源恢复/数据一致性/ownCID清理通过。高负载丢弃作为饱和信息保留，不称生产SLA或整轮通过。证据70b50ecd-8d1f-487b-8713-772aa741e68e/fullraw329e6ae4及三报告；实际联合PG4CPU2GiB/k64CPU4GiB，旧2CPU全部失败保留。下一仍需原基础/规模完整五阶段串行门控及86400，整体未收口；本条时正式两档和24h尚未开始。


## CP293 · 当前完整回归与本地跳过审计

当前Go93f/实际非race6d298/runtime338：289c7b57原五suite真实22:15:57→22:44:35Z，2602P0F79skip/blocked保留，五Go命令exit0/race/count1。可见QA独立审79原名/原因与旧批次相同；root核五command completion原JSON30+入口16+新smoke3，49原本地skip已证明当前通过。其余18无测试包、8暂停CI-CD、3付费行（2名）及1Ollama容量仍可见。审计515c5095/人工说明5105d522不计验收，acceptance-plan显式登记且实际8787读回：regression incomplete、42/166主正常、soak not_started、overall open。旧2556P及旧smoke10P不冒当前。

a8071b5a指标问答原完整组133P0F0skip，真实Prometheus+模型协议Mock，原240s超时/权限/SSE/撤权/提交失败/一致性和精确清理；证明282c1187及三报告。c5c279c7三个原组件Go opt-in实际race/count1共10P0F0skip，当前Keycloak/Prom/Loki/Tempo/UUID PG，原CLI与等待/清理不改；证明7b886265及三报告。只改私有wrapper的GO/BINARY绑定，不调用旧硬编码helper，不增加正式接口或24h资格。

fresh53doctor/HMAC/UID/fullargv/lstart/cwd及实际PG4CPU2GiB、固定k64CPU4GiB、exactBASE11973905、VM11.1980idle/8.3406GBavailable/cert90000通过，新freeze绑定当前93f/main6d298/perf2e11。正式singlecommand BASE a21ab950于23:04:57.553272Z启动，原120s10→300s10/25/50→60s100及原pureDB25门槛/SSE/health/quiet60/一致性/清理不改，无extraobserver，fresh唯一runner独占caffeinate随退出。工具Store根及OID JSON类型首次失误保留，未变canonical结果/资源；只读准入修正精确字符串比较后绿。

正式BASE尚在执行，支持路径全P且仅已知HomecapB才显式SCALE；默认双command禁止。原ffe/b972/cdd/7cab首红留、短dcfa通过不代完整，PERF-SSE-100-FFE5仍needs_confirmation。真实24h从未启动，WAN152/Trace/权威producer/真实租户/paid/Ollama/暂停CI-CD仍高TODO。产品web、用户.claude、业务环境与真实通知不动；不merge/deploy。具体最新运行及证据见CHECKPOINT CP293和coordinator-current真实SQLite。
