# 缺陷收敛：缺陷与轮次记录（2026-10-16 起）

计划：`docs/plans/2026-10-16-defect-convergence.md`（2026-10-16 用户批准第 9 节全部推荐）。Linear 父任务 WAN-434，每条缺陷一个子任务；决策记录见 Linear「决策｜缺陷收敛：覆盖门槛加连续轮次」。上一轮登记见 `docs/releases/2026-10-09-production-acceptance.md`（PA-01～PA-104，D1～D45）。

**分级**沿用验收计划，并按计划 2.3 补充定级示例：越权、数据错误为 P0；核心闭环页面整页崩溃、白屏或整页 503 至少 P1；界面有入口、点了却是 501 或接口未注册，按缺陷定级，不算缺能力。

**字段**：编号（PA-105 起）、级别、问题、处理与验证、新颖性（首次路径 / 首次数据状态 / 首次角色 / 重复路径 / 修复引入）、发现方式（回归 / 演练 / 任务卡 / 自由探索 / 用户亲测 / 审查 / 静态比对）、阶段或轮次。

## 缺陷清单

| 编号 | 级别 | 问题 | 处理与验证 | 新颖性 | 发现方式 | 阶段/轮次 |
| --- | --- | --- | --- | --- | --- | --- |
| PA-105 | P2 | 调查画布“证据托盘”拖放到画布会调用 `POST /investigations/{id}/evidence`（`CanvasPage.tsx:793`），这条路由只在旧版注册（`internal/httpapi/pin_evidence.go`），没有标 V3，V3 下返回 501 `v3_not_implemented`；界面有入口，按 2.3 算缺陷 | 待修。10-16 prod 实例确认 501；拖放后的界面提示未在浏览器里验证。Linear WAN-435 | 首次路径 | 静态比对 + 实例确认 | 0 |
| PA-106 | P2 | Webhook 页“重新投递”按契约暂不启用（mock），后端未注册 `POST /webhooks/{id}/redeliver`，真实模式下按钮仍显示，默认选中项写死为 `gitlab`；请求落到 GitLab 入站接收路由返回 401，前端把业务接口的 401 当会话过期跳登录页（`client.ts:96`）。同 PA-21、PA-10 一类 | 待修。10-16 prod 实例确认 401 `unauthorized`；跳登录页按前端代码推断，未在浏览器里验证。Linear WAN-436 | 首次路径 | 静态比对 + 实例确认 | 0 |
| PA-107 | P3 | 模型调用失败时服务端不记录底层原因：10-16 07:53:20Z 交付演练里画布 AI 回复约 29 秒后失败 `failure_code=model_unavailable`，接口 503、`attempts` 可见可重试（符合设计），但服务端日志没有上游状态码、超时还是连接失败、耗时，审计详情 `changes: redacted`，分不清 DeepSeek 自身故障还是产品侧问题（计划 2.3 的 AI 规则要靠这个区分）。PA-87 只覆盖了链路与日志读取 | 待修。Linear WAN-437 | 首次数据状态 | 演练 | 0 |
| PA-108 | P3 | 多个写接口先查来源能力、后查权限（10-16 第二轮探测扩大范围：`POST /alert-rules`、`/alert-rules/import` 两实例 503 `not_connected`，nonprod 跨环境 `/alert-rules/{id}/disable` 503 `enable_not_connected`）。最初发现：nonprod 新建静默先查来源能力、后查权限：test 环境没接静默写入源，只读成员 `POST /silences`（空或合法请求体）直接 501 `not_connected`，没有先 403/404；抑制规则有 `TestInhibitionDeniedBeforeSourceCapabilityCheck` 要求先拒绝。暴露“该环境没接写入源”这一配置状态 | 待修。Linear WAN-438 | 首次角色 | 权限矩阵（合法请求体） | 0 |
| PA-109 | P1 | 真实 Runbook 详情页整页崩溃：后端无可信来源时 `usage.refs/resolve_rate/weekly/highlight`、`incidents` 为 null（`unknown_fields` 列出），前端类型声明为必有值，`rb.incidents.map`、`VBars` 收到 null，错误边界替换整页（两实例 6 个 Runbook）；同处写死示意文案“近 12 周 · 高亮为 INC-1042 所在周” | 已修复（01ef1fce）：类型可空，空值显示现有 `UnknownValue`，无周序列不画图，INC-1042 那句只在示意数据模式下显示。已验证：详情页对拍 116 次加载 0 个未捕获异常，prod 截图 `shots/pa109-*` 显示“—”。Linear WAN-439 | 首次路径 | 回归（T5 详情页对拍） | 0 |
| PA-110 | P3 | 生产端 `GET /config-sync` 路由登记为 `workspace.view`，但处理函数按设计只对人类平台管理员 + 全局 `workspace.manage` 开放；只读成员、运维人员、审批员入口照常可点，点进去是 403 接口错误，不是“禁用并提示所需权限”（D10）。同 PA-56 一类，无越权 | 待修。Linear WAN-440 | 首次角色 | 回归（T4 多角色对拍）+ 权限矩阵 | 0 |
| PA-111 | P3 | Runbook 详情的适用范围显示 `app:<应用内部 ID> · env:prod`，不是服务名与环境名（同 PA-37 一类） | 待修。Linear WAN-441 | 首次路径 | 回归（T5）复核截图 | 0 |
| PA-112 | P3 | 生产实例 Runbook 详情的试跑写着“在预发用只读步骤走一遍”“在预发试跑只读步骤”，生产实例只有 prod；需先核对 dry-run 在生产实例的实际行为再改文案 | 待修。Linear WAN-442 | 首次路径 | 回归（T5）复核截图 | 0 |
| PA-113 | P3 | 中文界面下，从复盘起草（经配置提升导入）的 Runbook 步骤标题是英文（“Review: Incident summary” 等），同 PA-24 一类；需查清是起草模板写死英文还是导入时丢了语言 | 待修。Linear WAN-443 | 首次数据状态 | 回归（T5）复核截图 | 0 |
| PA-114 | P2 | 模板页“新建模板”对话框列出全部画布并默认选中第一项（真实数据里是调查画布），保存 `POST /canvas-templates` 404“Source canvas not found”，前端未处理（未捕获的 `ApiError`），无任何提示、对话框不关；从通用画布保存可以 201，但提示写“团队模板”，实际是私人画布模板，且不显示在本页（本页列调查模板，空目录无说明）。WAN-125 实测结果 | 待修。扫荡截图 `e2e/sweep/wan125-save-template-*.png`。Linear WAN-444 | 首次路径 | 扫荡 | 1 |
| PA-115 | 缺能力 | 调查模板没有创建途径（后端只有列表、编辑、启动），任何部署里目录都为空，“从模板启动调查”入口不可达；潜在缺陷：启动请求不带契约要求的 `inputs`（WAN-128） | 交用户决定是否补（需设计图）；不补则模板页说明（并入 PA-114）。Linear WAN-445 | 首次路径 | 扫荡（代码核对） | 1 |
| PA-116 | P3 | `POST /canvases` 新建通用画布缺 `services` 时写库 NULL 撞非空约束，统一包成 503 `canvas_unavailable` 且不记日志；应 422 或当空列表。前端始终带 `services`，用户路径不受影响 | 待修。Linear WAN-446 | 首次路径 | 扫荡 | 1 |
| PA-117 | P3 | 收件箱认领成功提示“已认领，值班升级已停止”（`InboxPage.tsx:45`），V3 没有值班升级（`/oncall-schedules` 503），同 PA-21/PA-30 一类 | 待修：真实模式只写“已认领”。Linear WAN-447 | 首次路径 | 扫荡（代码与接口核对） | 1 |
| PA-118 | P3 | 已恢复的告警“认领”仍可点，409 `already_claimed_or_closed` 弹出后端英文原文（同 PA-24 一类） | 待修。扫荡截图 `e2e/sweep/alert-claim-*.png`。Linear WAN-448 | 首次路径 | 扫荡 | 1 |
| PA-119 | P2 | 告警组过往的已恢复发生记录挂在两个以上不同调查上（演练后事件没结案很常见）时，对正在触发的组“发起调查”（行内或批量）恒定 207 `conflict`：`activeInvestigationMember` 只看事件状态 open/investigating、不看告警是否仍在触发。prod checkout-api 的 `LabHigh5xxRate` 组 12 条事件里两条挂着 `INV-e0d3abd4…`、`INV-85425a14…` | 待修，**修复需定方案**（改分组调查的成员口径属改变产品行为，交主会话与用户确认）。绕行：事件页发起调查（本次未验证）。Linear WAN-449 | 首次数据状态 | 扫荡（故障窗口） | 1 |
| PA-120 | P3 | 告警页批量/行内动作：未完成项的提示直接显示后端代码（“1 组未完成：conflict”“2 组未完成：not_connected”，`f.error \|\| why(f.outcome)` 跳过了中文映射），批量静默按设计 `not_connected` 也不说明原因与替代入口；动作对只读成员不门控，点了提示“not_found” | 待修。扫荡 `alerts-bulk`、`alerts-viewer`。Linear WAN-450 | 首次路径 | 扫荡 | 1 |
| PA-121 | P3 | 静默提案（创建与提前结束）审阅页的标题、摘要、核对清单、审批要求、执行方式、依据、恢复验证都是生成时写死的英文；摘要“…if a separate approval and execution flow is later enabled”“No silence is created by this proposal”已过时（批准后约 5 秒静默即生效），会误导审批人。同 PA-24 一类 | 待修。提案 `ca60e8a3…`、`d7f03f5b…`。Linear WAN-451 | 首次路径 | 扫荡 | 1 |
| PA-122 | P3 | 审阅页“讨论”回复框对只读成员可用，发送 403，`onSubmit` 未捕获（页面异常 `ApiError`）、无提示；评论要 `proposals.change`，变更审批员同样 403（审批人能否评论是产品口径，修时确认）。拒绝/批准按钮门控正确 | 待修（门控按现有权限做）。扫荡 `review-viewer-comment`。Linear WAN-452 | 首次角色 | 扫荡 | 1 |
| PA-123 | P2 | “新建抑制规则”两实例都不可用：预览不带契约要求的 `application_id`，恒 404 且错误被吞、预览区一直空白（带上后 prod 为 501 缺七天历史来源）；保存 501 `not_connected`（未接 GitOps 抑制仓库），提示为英文原文；不带 `If-Match`；入口未按 `silences.manage` 门控，“生效范围”不进请求。按 2.3 算缺陷 | 待修，**修复需定方案**（来源未接时隐藏、标暂不可用或补接来源；新元素需设计图）。扫荡 `inhibition-drawer`。Linear WAN-453 | 首次路径 | 扫荡 | 1 |
| PA-124 | P2 | 新建 Runbook 页（WAN-127，界面上唯一入口）真实模式只能存空白草稿：“从复盘生成”的候选取自 `/postmortems/candidates`（契约是“尚无复盘的已结案事件”），两实例已发布复盘共 5 篇、候选却为空；模板/对话/导入没有来源选择控件，`/runbooks/draft` 缺 `from` 恒 422；起草失败不提示，仍显示“AI 起草”和写死的示意提示；保存不带 `from`/`expected_source_sha256`，422 未捕获；空白起点保存时丢掉输入的标题（非受控输入，存成“未命名 Runbook”），不能加步骤；步骤类型 `action`/`act` 不一致。起草步骤标题写死英文“Review: …”即 PA-113 的来源 | 待修，**部分需定方案**（模板/对话/导入需新来源选择控件或真实模式先隐藏）。扫荡 `runbook-new`，产生 Runbook `ee36a525…`。Linear WAN-454 | 首次路径 | 扫荡 | 1 |
| PA-125 | P2 | 第二位审批人批准静默提案后，弹窗主按钮“查看执行进度”跳 `/executions/<静默执行 ID>`，该接口只投影回滚与旧动作执行，404，执行页整页显示“暂时不可用 · 来源连接失败或超时，稍后会自动重试”（原因不实、不会恢复）；实际已执行成功（审阅页执行框“已确认”、Alertmanager 已有静默）。审阅页只对回滚/规则显示“查看执行”，弹窗未同样判断 | 待修。定级 P2（执行页对回滚正常、结果在审阅页可见），按 2.3 字面可读成 P1，留审查复核。扫荡 `exec-pages-prod`。Linear WAN-455 | 首次路径 | 扫荡 | 1 |

## 阶段 0 记录

- **2026-10-16 环境恢复**：只读盘点发现 Vault（开发模式）已停、凭据丢失；验收二进制为 6b206d22 且 `vcs.modified=true`。处理：实验三台虚拟机 `cluster.py up`；启动实验中间件、观测与企业组件容器；`enterprise/vault.py up` 重新注入 7 个路径（回滚令牌新签 24 小时）；`delivery/lab.py resume`。验收实例改用干净构建：Go 二进制在临时 worktree 中从 5502f631 构建（`vcs.modified=false`），Web 从同一提交构建；旧产物备份为 `.local/acceptance/bin/aiops.6b206d22-dirty` 与 `.local/acceptance/web.6b206d22-dirty`。6b206d22 之后没有新迁移（最新仍为 179，已在两个验收库执行）。

- **2026-10-16 基线回归**（5502f631 干净构建，`regress-cv-baseline.json`，含 `--with-golden-api`）：19/19。
  - 对拍 216 次加载中 211 次干净，0 个未捕获异常、0 个空白页；4 个接口失败组都在 prod：`GET /services` 503 `service_view_unavailable`、`GET /coverage` 503 `coverage_unavailable`、`GET /service-mappings` 503 `authorization_unavailable`、`GET /config-sync` 503 `gitlab_unavailable`。
  - 对拍期间宿主 1 分钟负载约 10，5 分钟平均在结束时升到约 21.5（对拍 6 个 worker，加上宿主上其他 kind 集群）。15:07 负载约 8 时逐个复查，四个接口都是 200（`/config-sync` 4.2 秒，其余 0.1～0.2 秒）。
  - 归类：`/config-sync` 失败在 14:58:50，服务端日志里全部候选都是 `gitlab_status=0`，当时 GitLab 刚恢复、API 还没就绪，算实验环境问题；另外三个在负载正常时复现不了，服务端没有记录原因，形态同 D45，记为性能轮输入（按 D46）。
  - 另见服务端日志：14:54、14:56 静默执行器轮询批准两次 `context deadline exceeded`（实例刚启动加对拍负载），记为性能轮输入。
  - 权限矩阵两实例 0 不一致；但只覆盖 138 条路由，对象授权写路由“结论不明” 68/70 条，因无真实 ID 跳过 40/44 条，与计划 1.2 的估算一致，待 T1。

- **2026-10-16 独立套件**（5502f631，`cv-baseline/summary.json` 与各步日志）：26 项中 23 项通过，3 项失败均已查明不是产品缺陷（见下）。下面逐条记录核对结论：
- drill-pod-crash（15:18:40）紧跟 pod-oom（15:13:50 起，同一 Pod checkout-api-…-9tt47），15 分钟取证窗口把 OOM 演练的 OOMKilled 样本带了进来，AI 主假设 OOMKilled（中等置信，并写明与 Error 并存未定）。测试设计问题，非产品缺陷；套件结束后隔开重跑一次。阶段 2 每轮只做一个演练，不受影响。
- 其余四个实验故障演练 AI 结论正确：重试风暴（源头 checkout-api、受害 order-api）、Pod OOM（OOMKilled）、消费者停止（consumer_paused）、节点压力（worker2，实测施压节点为 worker2）。
- rollback-prod-ui 23/23；回滚反例 faulty-target、restart、drift、revoked 通过。
- rollback-edge-write-forbidden 失败：nonprod message-api 回滚候选为空、`blocked_reason: write_not_approved`（10-10 时按旧写入绑定可走到 Kubernetes 拒写）。统一登记 P3/P4 后执行回滚需要已批准的写入目标，产品提前拦下并给出原因，属于更早更安全的拦截；反例前置条件过时，放进阶段 1 在统一登记下重新搭建。另：`rollback_edges.py` 提案失败后没有停，带空提案编号进批准步骤导致 TypeError（工具问题，P3 级，修）。
- delivery-drill 失败（15:52～15:57）两个原因：① 实验 `d11.py release-bad` 发现 MR !9 已合并就“按原样报告不改动”，今天没有新部署，变更列表/修订对应/回滚目标连带失败——前置条件问题，需先 `d11.py bad-version --again` 开新一轮再跑；delivery_drill.py 应自动处理（工具修）。② 画布 AI 回复 07:53:20Z 起约 29 秒后失败 `failure_code=model_unavailable`，接口 503、attempts 可见可重试（符合设计），服务端日志无底层原因 → 登记 PA-107（P3，WAN-437）。重跑后再判定。
- d11-negatives-changes 10/11：唯一失败项是脚本按标题取了第一个 LabHigh5xxRate 事件（order-api，没有 GitLab 项目与工作负载绑定），返回 `gitlab: not_configured` 与空列表是正确行为；换 checkout-api 事件 `gitlab: complete`、1 条变更、2 条时间未知修订。工具问题：按服务挑事件。
- d11-negatives-rules 19/19；config-sync 44/44；faults loki/tempo/prometheus/elasticsearch/alertmanager 全部 PASS。
- outage.py 不论成败退出码都是 0（D28 只改了 faults.py），要读日志里的 `"ok"`；model ok=true（断开时 failed、恢复后 completed），database ok=true（停库 20 秒内每次 503 session_store_unavailable、≤0.3 秒，恢复不需重启）。工具修：失败时退出码非 0。
- outage-kubernetes ok=true，但只测了重启后几秒内（前 5 分钟按设计显示最近快照，200），记录的是 `unavailable` 字段而不是设计里的 `collection.state`；超过 5 分钟的 `stale` 与前端提示（PA-61）这次没覆盖 → 进阶段 1 覆盖清单；工具修：读 `collection.state`，并加一个等过 5 分钟的选项。
- 工具合并后验证：代理访问日志生效（JSON 行，无查询串和 Cookie）；credentials.py 9/9（GitLab 管理员令牌 11-10、配置仓库令牌 11-11 到期）。
- 权限矩阵 257 条路由：prod 742 次探测、nonprod 710 次，空请求体下 0 条真实不一致；prod 27 / nonprod 11 条“不 ok”逐条核实都不是缺陷：OIDC 绑定路由运行时未注册（验收实例没开 OIDC，404 page not found）；`GET /config-sync` 生产端按 `docs/api/v3-config-sync.md` 只对人类平台管理员开放；Runbook 草稿与待审只有维护人可见（列表与详情一致）；MCP 列表为空、评测未执行导致样本 ID 不适用。工具要排除：运行时未注册、仅维护人可见、无样本。
- 合法请求体探测（D47）：prod 117 次（拒绝 88、请求体不合格 27、guard_hit 2）；nonprod 108 次（拒绝 84、不合格 20、guard_hit 2、不明 2），跨环境“无权对象”4 条全部拒绝。guard_hit 是画布节点 PATCH 缺 `expected_layout_version` 先返回 428（请求体校验先于鉴权，补上过期版本后无权角色 404），工具表要补字段。未发现越权。nonprod 静默新建先返回 501 not_connected → PA-108（P3，WAN-438）。
- **2026-10-16 工具合并与修复**：两个工具分支按用户同意的顺序合并（da3ed7d0、66828adb）；4 个演练脚本的问题已修（f1631991）；`audit_writes.py` 首次实跑因 `json_agg` 换行解析失败，已修（ab12b225），prod 5282 条审计、112 种动作，nonprod 1667 条、76 种。
- **2026-10-16 回归（含多角色与详情页对拍，`regress-cv-tools.json`）**：14/21。管理员对拍 216 次全部干净、0 失败组（对拍降到 3 个 worker 后负载 8～12，D45 类超时未出现）；只读成员、运维人员各 108 次、107 次干净，唯一意外项为 PA-110；详情页对拍首次运行查出 PA-109（P1，已修复验证）。其余失败是工具问题：权限矩阵的假阳性（交 t1-route-matrix 修）、详情页对拍用错告警组和评测用例的地址、管理员入口门控规则（交 t2-coverage-tools 修）；韧性接口 503 按契约（五类来源未配）加入 `crawl-exemptions.json` 豁免。
- **2026-10-16 重跑与第二批工具修复**：Pod 崩溃演练隔开重跑后 AI 判断正确（终止原因 `Error`，无 OOMKilled）；交付演练自动开第 3 轮后 25/25（AI 29 秒作答、建议 84 → 83、两人批准回滚并验证通过、修复上线后无告警）；D11 变更反例 11/11。两个修复分支按用户同意合并（cbb17df7、8b1fffef）。实例验证：权限矩阵两实例 0 不一致（PA-110、PA-108 作为已登记缺陷单列；`GET /canvases/{id}` 的样本全是调查画布 ID，通用画布详情在真实数据上未覆盖，列入阶段 1）；合法请求体探测 prod 133 次（拒绝 121）、nonprod 132 次（拒绝 117），无越权，PA-108 范围扩大到告警规则新建、导入与停用；管理员对拍 216/216 干净、门控问题 0（`proposals.approve` 按设计只列备注）；详情页对拍干净重跑 98/98、6 个韧性 503 按契约豁免。详情页仍跳过 5 类：prod 审阅页与执行详情（提案没有列表接口，取不到 ID）、两实例评测用例（评测未执行无用例）、nonprod Agent 运行（无数据），列入阶段 1 覆盖缺口。

## 阶段 1 记录

结论写进 `.local/acceptance/coverage-verified.json`（六层：页面、数据、权限、环境、异常、AI）；扫荡场景在 `.local/acceptance/sweep-scenarios/`，输出与截图在 `.local/acceptance/e2e/sweep/`（`web/e2e/sweep.spec.ts`）。

- **10-16 19:10 开工**：Go 二进制为 5502f631 干净构建（`vcs.modified=false`），Web 为含 PA-109 前端修复（01ef1fce）的构建，两实例健康；5502f631 之后的产品提交只有 01ef1fce（仅前端）；`clock-watchdog.py check` READY；凭据 9/9（回滚写令牌 10-17 06:52Z 到期）；宿主 1 分钟负载 10.0。
- **1.7 第 1 步 · 核心闭环（10-16 19:12–19:35）**：在 aiops-test 开重试风暴（19:12:39 开，19:13:52 Prometheus 触发，19:14 产品两组触发），故障窗口内：
  - 认领：运维人员在收件箱认领正在触发的 checkout-api 告警 200，刷新后仍为“已认领”、计入“待我处理”，审计 `alert.claim`；提示仍是“值班升级已停止”（PA-117 复现，不重复登记）。并发：运维人员与管理员同一修订同时认领，一个 200、一个 412 `claim_revision_changed`；之后另一人用新修订认领 409（英文原文，PA-118 一类）。只读成员、审批员认领 403；不存在的告警 404。
  - 批量：认领两组 200“已认领 2 组”；发起调查 207（order-api 新建 `INV-d3d3556c…`，checkout-api `conflict` → PA-119）；提议静默两组 `not_connected`（按设计，缺专用分组标签声明），提示显示原始代码 → PA-120。只读成员批量 207 逐项 `not_found`，入口不门控（并入 PA-120）。nonprod 全部环境视图 409 `env_view_only`，跨实例分组 ID `not_found`。
  - 静默：抽屉预览“当前匹配 1 条告警”，与 Alertmanager 一致；改成不存在的告警名后 0。抽屉提交提案 202 → 审批员 1、2 通行密钥批准 → 约 5 秒后 Alertmanager 出现静默 `0e26a304…`，产品静默列表“生效中、影响 1”。提前结束：静默页“提议提前结束”选调查、写原因，`DELETE /silences/{id}` 202 生成取消提案 `d7f03f5b…`（走提案，不直接执行，符合契约）→ 两人批准 → 约 5 秒后 Alertmanager 中该静默 expired，产品列表同步。对已结束的静默再提取消 409 `silence_proposal_conflict`；只读成员取消 404。静默生效的 1 分钟内告警组状态仍为 firing（`silence_state` 在 `unavailable` 里，按契约）。
  - 抑制：预览与保存两实例都不可用 → PA-123。
  - 提案评论：发起人评论 201，刷新后仍在；空评论 422；只读成员 403 未捕获、审批员 403 → PA-122。审阅页拒绝/批准按钮对非审批角色禁用并写明所需权限。审阅页内容英文且过时 → PA-121。
  - prod 审阅页与执行详情：三条回滚执行页（`158fb8d4…` 等）正常显示时间线、验证采样、副本与批准人；静默执行 ID 进执行页整页“暂时不可用” → PA-125。
  - 执行报告发布：V3 旧动作 Worker 关闭，只能投影已有持久执行；三条 prod 回滚执行 `report` 为 null，发布按钮不出现，界面无入口 → 归**缺来源**（不登记缺陷）。
  - 新建 Runbook 页（WAN-127）→ PA-124。
  - **实验环境**：19:21 起 worker2 不可达，看门狗 19:24 判定、19:26 重启修复（`repair_done`，80.6 秒，证据 `aiops-test/.local/clock-watchdog/evidence/20261016T112444Z-aiops-k8s-worker2`）；期间 `lab-faults.py off` 超时，修复后关闭成功，故障窗口 19:12:39–19:27，所有开关 off。不计产品缺陷。
  - 工具：`sweep.spec.ts` 增加 `fillPlaceholder`、`fillSelector` 两种步骤。
  - DeepSeek：本步 0 次调用（没有触发 AI 调查）。

## 轮次记录

（阶段 2 开始后每轮一节，由 `tools/acceptance/round.py` 生成段落。）

## 自主决定（待 review）

| 编号 | 时间 | 决定 | 理由 |
| --- | --- | --- | --- |
| D46 | 10-16 15:20 | 计划 2.3 的超时规则只写了“1 分钟负载超过 24”的情况，没写 12～24 之间怎么算。补充为：超时发生时 1 分钟负载达到 12（本机核数）及以上，就和超过 24 一样处理——要在负载低于 12 时复现一次，复现不了的记为性能轮输入，并计入该轮的负载豁免次数；发生时刻的负载不知道时，取该步骤采样到的最大值 | 基线对拍本身（6 个 worker）加上宿主上其他 kind 集群，就能把负载推到 10～21，按原文这些超时既不算“超过 24”，也说不上“负载正常”，没法归类。保留“负载正常时复现”这个要求，是为了不把真实的功能缺陷当成负载问题放过去。另：对拍的 worker 数和其他 kind 集群是否暂停，会直接影响豁免次数，后者等用户决定（10-16 用户定：不暂停，对拍 worker 6 → 3） |
| D47 | 10-16 15:30 | T1 合法请求体探测里的“无权对象”改用跨环境对象：在 nonprod 用 `permissions.py --setup-scoped dev` 建一个只在 dev 有写权限的自定义角色和探测账号，带 dev 的环境头去写 test 环境里的对象；探测只用没有该授权的角色，每条路由都配了防写入的保护（过期版本号、已结束状态的目标、不存在的引用等），找不到保护的 19 条路由跳过并写明原因 | 计划写的是“一个有权对象、一个无权对象”，但自定义角色目前只能限定到环境、不能限定到应用，按应用做不到；跨环境是现有模型里唯一能表达“无权对象”的方式。建角色和账号是验收实例上的写操作，在 10-09 授权范围内 |
| D48 | 10-16 18:00 | WAN-125（从画布保存模板）、WAN-128（从模板启动调查）这类早期登记、至今没接通的前端入口，阶段 1 按计划 2.3 计为缺陷（在实例上确认后登记为 PA），不按计划 4.2 的写法归为缺能力；WAN-126、WAN-127 已部分修过，阶段 1 实测后按同样规则处理 | 计划里两处写法冲突：4.2 说“没被后续工作覆盖就归为缺能力”，2.3 说“界面有入口、点了却失败算缺陷，不算缺能力”。这几个入口用户看得见、点得到（10-16 实测更正：WAN-125 的“复制”按钮已在 PA-21 时改为只在示意数据模式出现，没有假动作；保存对话框的问题登记为 PA-114；WAN-128 实测为缺能力 PA-115），按 2.3 处理与 PA-88 的定级一致；独立审查也指出过这一点 |
| D49 | 10-16 19:00 | （用户 10-16 确认）阶段 1 扫荡交给一个可见子会话执行（计划第 6 节），并在阶段 1 期间由它独占这个检出目录：登记册、`coverage-verified.json`、扫荡用例与验收工具、P2/P3 与原因明确的 P0/P1 修复都由它直接在 main 上提交并推送，构建并部署到验收实例也由它做（它是唯一在实例上跑测试的会话）；主会话只读、复核它的汇报，不在这期间改仓库。原因不明的 P0/P1、需要新界面元素或改变产品行为的修复，它停下来交主会话和用户决定 | 计划第 6 节写的是“合并、构建、部署都由主会话做，子会话不部署”。但扫荡要频繁在“发现 → 修 → 部署 → 复测”之间往返，按原写法每一轮都要等主会话合并、等用户同意合并新分支，单线执行也保证不会有第二个会话同时在实例上测。改为子会话独占检出目录，比两个会话在同一目录交替提交更不容易冲突 |
