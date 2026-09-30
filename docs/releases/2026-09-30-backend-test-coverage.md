# 2026-09-30 后端测试覆盖与跳过项分类

本记录对应 WAN-138 / WAN-136。只读核对 SQLite、Go JSON、正式接口 JSONL 与实际测试源码；不修改历史运行结果。跳过、环境阻塞和外部待办均不计通过。

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
