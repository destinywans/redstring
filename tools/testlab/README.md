# 后端测试实验室

[实施基线](../../docs/plans/2026-09-30-backend-testlab.md)。仓库根目录执行，不读取产品.env。私有凭据/运行数据仅在.local/testlab/。

```sh
python3 tools/testlab/testlab.py doctor
python3 tools/testlab/testlab.py prepare
python3 tools/testlab/testlab.py serve
# 另一终端
python3 tools/testlab/testlab.py run --suite static
python3 tools/testlab/testlab.py run --suite baseline,mfa,passkey,configsync
python3 tools/testlab/testlab.py run --suite regressions,query-plan,entry,components
python3 tools/testlab/testlab.py run --suite content
python3 tools/testlab/testlab.py run --suite readonly,discovery,management
python3 tools/testlab/testlab.py run --suite promotion
python3 tools/testlab/testlab.py run --suite identity,invitations,vault
python3 tools/testlab/testlab.py run --suite local-live-gates,jenkins
python3 tools/testlab/testlab.py run --suite gitops
python3 tools/testlab/testlab.py run --suite faults
python3 tools/testlab/testlab.py run --suite claim-concurrency
python3 tools/testlab/testlab.py run --suite performance
python3 tools/testlab/testlab.py run --suite soak
python3 tools/testlab/testlab.py report --run-id <运行ID>
```

只读看板：http://127.0.0.1:8787。full包含全部测试和真实24小时窗口，跳过/阻塞/待办不计通过。参数详见--help。

依赖Python3、Go、Docker、OpenSSL；重型GitOps需要kind、kubectl；压测固定k6，各组件固定镜像。资源/端口不复用；cleanup须匹配创建环境的run-id，仅清理自有资源并保留卷和证据。测试源码未冻结时看源码审计。Mock/软件认证器不证明真实模型、账号、物理认证器或租户。

prepare 一次性准备自有PG、轻型真实组件、证书、正式双V3账号、规则隔离/Webhook和基础档数据；执行中的测试会阻止重启/重置。GitLab/Argo按独立重型工作包串行；镜像缺失或资源不足明确报阻塞。规模档数据已存在时不可复用基础档验收，必须新建对应自有环境。

登录代理配置 AIOPS_TRUSTED_PROXY_CIDRS 只填真正直接连接到API的代理CIDR；空值忽略X-Forwarded-For。固定 loopback 测试代理仅用于本实验环境。

条件式正式链只使用本轮生成的私有账号/配置/证书。`identity` 使用独立 HTTPS Keycloak，不放宽 Secure Cookie；`invitations` 使用专有TLS465捕获器，不向真实邮箱发送；`vault` 用实际 least-policy KV 和 TLS受控上游验证六scope绑定。`content` 通过真实上传URL/注册/同步、模板复制和事件时间线，SQL仅提供明示合成graph和本轮application-bound未发布Runbook draft/v1 snapshot输入，不伪造审批/发布结果。Jenkins原live必须读回唯一实际build，不能以仅包pass或skip代替。首次红、primary与cleanup错误分别保留，临时组件均按owner/run/image/name清理。

`claim-concurrency` 使用自有PG随机schema和实际V3路由handler，覆盖列表暂停期间首次认领/同用户重试、人工事件状态CAS、创建关联去重、版本与审计、作用域漂移、等待期间撤权/会话到期及policy两阶段复核；结果按Go JSON进入看板。该专项不代替正式非race二进制与原两档k6性能验收。聚合读捕获单次语句的告警投影，并持有当前授权锁；读快照可能先于同时完成的认领。通知作用域不一致仍失败关闭。

执行中的 custom helper 会继续登记用例；暂时3/3不是整个组结束。整体结果以持久run/suite终态为准。环境block、外部待办、缺实现、跳过均不计通过；`full` 有这些结果就不能宣称全部验收完成。主166条契约与407历史库存分开核对，新增正常method/path仍不能证明所有输入和失败时序。


性能首红证据保留操作使用 `profile.py`，只允许专属owner的两项明确上下文：06a848d7原failed运行，或8ac896b5原interrupted且基础rps25实际failed运行。第二项严格绑定Go1512、Git d6f7bd1、原APP OID9270802和固定三份0600 receipt；解析与SHA使用同一稳定FD字节批次，任何读取漂移失败关闭。8ac原SQLite缺末尾source审计，后停观察以actual_post_stop_observation明确记录，不补造原run字段。先用一个固定UUID仅读plan，确认无活动测试、真实HMAC进程/容器/五DSN归属，再同参数加 `--apply`。partial恢复必须沿用原UUID；日志0600保存在owner的profile目录，旧库以原OID留存，不DROP、不FORCE、不terminate PG backend。staging创建回执丢失而无可靠OID/tag证明时阻塞，不自动接管。apply上限40秒（quiescence8秒），超时保留WAL；实际进程和DDL结束不能因客户端超时而假定失败。成功只说明旧库留存和新空canonical/two extensions，始终ready=false，迁移/new binary/runtime需主会话另行初始化核验。

```sh
python3 tools/testlab/profile.py --owner testlab-3cbde3095125 --run <同一操作UUID> --before-evidence-run 06a848d7-6821-463f-96bd-1ee2639ea6f8
# 归属与独立审查通过后，在同一命令末尾添加 --apply；不得与活动测试并行。
```

第二项使用 `--before-evidence-run 8ac896b5-70c4-43c8-a934-309ce357e31d` 和一个新的固定操作UUID；旧eeb805fd操作已经完成，不重复归档。每次operation保留原库完整OID/数据，完成后的同UUID重放返回applied=false。工具离线测试与只读审查通过仍不能替代现场plan/apply证据。

固定七个路由的延迟/错误/次数指标用于定位真实HTTP瓶颈，不带用户、ID或URL高基数标签。阶段失败诊断仅访问已HMAC绑定的自有controller与两冻结API的`/healthz`，共用3秒墙钟预算；不带Cookie，仅保存允许状态及响应hash/长度，不改变原阶段失败和负载清理。

`python3 tools/testlab/diagnose.py` 仅用于准确基础档定位：固定原预热10RPS/120秒、10RPS/300秒、25RPS/300秒及原门槛，50正常/10慢读SSE与60秒重连、恢复/一致性核验。保留原混合负载、权限、超时及认领重试；要求exact base及冻结源码/配置/入口自身SHA。七路由指标定位实际HTTP延迟，不能代表全部阶段/规模档/24h；所有结果显式diagnostic_only和两个formal acceptance=false。SIGINT/SIGTERM交接及末段均返回interrupted/130。可通过私有固定manifest以testlab_json登记看板；现场准入与完整性能命令分别执行，不用诊断通过替代验收。
