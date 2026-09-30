# 后端 Review Top Risks 修复 · 2026-09-30

## 范围与决定

用户在生产级后端 Review 后明确要求先修复四项 Top Risks，并把 Needs More Context 登记为高优先级待办。基线为 `codex/v3-backend` 的 `09171b4f6f39befc97002a4990b5381aa32ce69e`；该基线尚未合入默认分支。本轮只修复四项缺陷与必要回归，不扩展前端、部署或真实外写。用户此轮授权取代上一轮仅源码 Review、不运行测试的限制，定向测试使用临时 PostgreSQL 和模拟外部依赖。

## 修复和证据

| 需求 | 修复 | 独立提交 | 当前证据 |
| --- | --- | --- | --- |
| [WAN-129](https://linear.app/wansss/issue/WAN-129/修复-oidc-身份绑定绕过原会话撤销) | OIDC bind/unbind 在治理、身份、用户及会话锁等待后重新验证原 Cookie 会话；授权完成后允许正常自绑定注销 | `6182b5a` | 注册 HTTP 正文等待期间注销、过期、停用和撤权；身份/会话锁跨到期；自绑定及 callback 互斥回归通过 |
| [WAN-130](https://linear.app/wansss/issue/WAN-130/修复-sse-慢客户端占用数据库连接与停机阻塞) | 先物化最多 64 个/4 MiB 事件并释放 Rows，再写网络；单事件超限不推进游标；每次写入/flush 使用 10 秒期限；15 秒停机宽限后 Close 活动连接 | `2409178` | 单连接池在阻塞写时可查询、字节有界不漏事件、真实 TCP 写超时、flush 错误停止、终态/重连/撤权及停机回归通过 |
| [WAN-131](https://linear.app/wansss/issue/WAN-131/修复多轮工具调用的流式正文与最终答案不一致) | 累计所有模型轮可见文本；每轮最终累积块只追加未展示后缀；工具响应不作为模型正文，真实正文分歧仍失败关闭 | `e2e9d29` | 四协议多轮工具、final-only/最终后缀、截断和持久化错误；真实 Runtime + 隔离 PG 的完成状态及下轮上下文回归通过 |
| [WAN-132](https://linear.app/wansss/issue/WAN-132/恢复批准过期后的告警规则未知外写结果) | 按历史动作/决议/双人投票及原 execution_id 只读对账，opened/merged 回执继续原观察链；新写仍需当前授权 | `dba3bbb` | 过期/撤销、claim 期间失效、未领取执行、查询故障、活动租约及旧 generation 晚返回、正常写入和观察；本地 TLS GitOps 与隔离 PG 生效状态回归通过 |

WAN-131 选择保留工具前说明文字，使历史正文与客户端已收到的流一致。等待整个模型轮再选择末轮正文会改变已有实时显示行为，因此本轮采用累计文本契约。WAN-130 的生产传输使用原生 net/http ResponseController；未来增加 ResponseWriter 包装时必须通过 `Unwrap()` 暴露期限能力。此期限适用于客户端写入阻塞，不是整个长连接的总寿命。

四项均已独立提交推送，集成在 `codex/v3-backend` 的 `b830ab4`。最终分支推送的回读与公共记录提交链接保存在 WAN-129～132；状态为 In Review，产品接受待用户。没有创建 PR、合并默认分支或部署。前三项集成后的受影响定向 `-race` 测试已通过：

```sh
GOCACHE=/private/tmp/aiops-review-go-cache \
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:15441/postgres?sslmode=disable' \
AIOPS_REAL_OLLAMA=0 \
go test -race ./internal/v3identity ./internal/v3conversation \
  ./internal/inference ./cmd/aiops \
  -run 'Test(Binding|OIDC|SSE|RegisteredSSE|EventStream|ConversationRegisteredJourney|ConversationSendAndSSERegistered|RealRuntimeToolStream|Runtime|ShutdownHTTPServer)' \
  -count=1
```

WAN-132 集成后完整规则模块 `-race` 通过（23.147 秒），入口停机回归再次通过，主程序最终构建通过：

```sh
GOCACHE=/private/tmp/aiops-review-go-cache \
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:15441/postgres?sslmode=disable' \
go test -race ./internal/v3alertrules -count=1
GOCACHE=/private/tmp/aiops-review-go-cache \
go test -race ./cmd/aiops -run '^TestShutdownHTTPServer' -count=1
GOCACHE=/private/tmp/aiops-review-go-cache \
go build -mod=readonly -o /private/tmp/aiops-review-server ./cmd/aiops
```

WAN-132 的合法回执恢复到 `pending_review`，原观察链仍要求独立审核、精确合并/同步字节及实际 Prometheus 规则，才能记录 `enabled`。查询故障保留可重试 `uncertain`；查无或不可信结果保留 `stale_remote` 并人工按原执行身份核对。查无不能证明先前超时的远端 POST 永远未执行，不能据此自动新建身份或补写。恢复增加 generation，沿用事务审计和条件更新；活动租约不被接管，晚到的旧 Prepare/Lookup 结果不覆盖新状态。正常 `Prepare` 继续严格要求 opened 回执；持久的 merged 回执只在只读 Observe 的局部参数中适配历史契约，原生 GitOps 实现仍 GET 核验真实状态。见 [规则写入与恢复契约](../api/v3-alert-rule-writes.md)。

测试库来自一次性本机 `pgvector/pgvector:0.8.6-pg17` 容器，仅监听 `127.0.0.1:15441`，没有挂载业务数据；各测试创建和删除随机 schema，部分 fixture 在此执行完整源码迁移。该地址只用于本轮验证，容器结束后关闭。需要复现时先启动新的隔离库并确认就绪，完成后停止它：

```sh
docker run --rm --name aiops-review-top-risks-pg \
  -e POSTGRES_HOST_AUTH_METHOD=trust -p 127.0.0.1:15441:5432 \
  -d pgvector/pgvector:0.8.6-pg17
docker exec aiops-review-top-risks-pg pg_isready
# 运行上面的定向测试后：
docker stop aiops-review-top-risks-pg
```

SSE 的真实 TCP 回归使用与生产相同的写入辅助实现及较短的测试期限。未执行业务库迁移，未访问真实 IdP、模型、GitLab、Argo 或 Prometheus，不把隔离回归作为产品验收。没有运行全量 make test/verify、前端构建、故障注入或修改实例开关。

## 高优先级上下文待办

以下均在 Linear 为 **High / Todo**，本轮只登记和界定证据，没有开始修改真实配置或运行外部验收。

- [WAN-133](https://linear.app/wansss/issue/WAN-133/高优先级核对部署代理实例开关与进程监督边界)：实际代理正文/SSE 超时、客户端地址信任链、PG 池、worker/进程监督和各能力开关。包括规则 worker 单次队列 DB 失败后退出的 MEDIUM 风险及部署是否补偿。
- [WAN-134](https://linear.app/wansss/issue/WAN-134/高优先级确认并修复反向代理后的密码登录共享限流)：核对 `account_layout.go` 基于 RemoteAddr 的 10/min 限流是否被代理聚合；不能直接信任任意 X-Forwarded-For。
- [WAN-135](https://linear.app/wansss/issue/WAN-135/高优先级核验-mfa-锁等待后的会话与-otp-到期边界)：MFA 锁等待后时钟、新鲜度、Session、Enrollment 与 OTP 到期，需结合实际 PG 等待限制定向复现。
- [WAN-136](https://linear.app/wansss/issue/WAN-136/高优先级补齐-v3-迁移与故障恢复的真实定向验收)：迁移台账、代表性数据、真实来源及跨批准期恢复，分别记录隔离测试、真实集成和产品接受。
- [WAN-137](https://linear.app/wansss/issue/WAN-137/高优先级复核仍在部署的-legacy-执行模块全链)：先确认实际启用和消费者，再检查旧动作/交付/部署完整链路。

## 其余 Review 结论

已确认的另两项 MEDIUM 风险未纳入用户本轮选定的 Top Risks 修复：Public Web 同步较旧快照可覆盖较新快照（`v3sourceregistry/sync.go`、`content.go`、`public_web_search.go`）；上传在读完慢正文后没有再次核验原会话和 conversation.change（`v3uploads/service.go`）。保持原 Review 结论，后续应各自建立独立修复与验收需求，不声称已解决。
