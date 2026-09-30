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
