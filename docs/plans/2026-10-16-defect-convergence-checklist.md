# 缺陷收敛：覆盖清单、任务卡池与轮换表（草案）

**状态**：草案，2026-10-16，待用户过目。对应计划 `docs/plans/2026-10-16-defect-convergence.md` 4.1 第 8～9 步；用户过目后开始阶段 1。Linear 父任务 WAN-434。

**依据**：阶段 0 的实测结果（`docs/releases/2026-10-16-defect-convergence.md` 阶段 0 记录）；T0 静态比对 `contract_check.py`；代理访问日志与 `coverage.py`；`audit_writes.py`（prod 5282 条、112 种动作，nonprod 1667 条、76 种）；`docs/api/v3-contract.md`；Linear WAN-125～128。

**怎么用**：
- 第一部分是阶段 1 的覆盖门槛。每一项按计划的六层标准（页面、数据、权限、环境、异常、AI）验证过一次，记进 `.local/acceptance/coverage-verified.json`（`coverage.py` 读它，只认“按六层验证过”，不认“调用过”）。
- 第二、三部分是阶段 2 每轮的任务卡和固定轮换顺序。偏离顺序要在轮次记录里写原因。

## 第一部分：覆盖门槛清单

### 1.1 写操作（前端会调用的 105 个写接口）

| 归类 | 数量 | 阶段 1 怎么处理 |
| --- | --- | --- |
| 阶段 0 已被脚本或用例调用过 | 63 | 多数只验证了接口返回；按六层补核对（页面提示、数据落库与回读、审计、权限、环境、异常） |
| 阶段 1 首次验证 | 40 | 第一次在真实数据上跑，历史上缺陷最集中的一类，优先做 |
| 已登记缺陷 | 2 | PA-105（钉为证据，V3 下 501）、PA-106（重新投递，未注册） |

逐条清单见附录 A。40 条“首次验证”按模块：

- 个人与账号安全（9）：头像上传与删除、偏好、TOTP 登记/确认/删除、通行密钥删除；
- 告警规则（4）：AI 生成、导入、预览、试跑；
- 记忆（4）：改事实、事实动作、冲突处理、策略；
- 静默与抑制（3）：静默预览、静默取消、抑制预览；
- 告警与事件（2）：批量操作、认领；
- 成员（2）：MFA 提醒、目录同步；
- 接入（2）：Pull/Push 测试；
- MCP（2）：工具设置、审核；
- 其余各 1：通知路由测试、链路视图删除、服务 SLO、知识源登记同步、提案评论、通用画布节点消息、执行报告发布、Prompt 试运行、Agent 工具登记、评测用例标注、护栏修改、从模板启动调查（WAN-128）。

`POST /config-sync/{id}/import` 字面匹配没找到，实际已被 `config_sync.py`（`mr-{iid}`）调用并通过 44/44，算作已调用。

### 1.2 页面与角色

| 范围 | 阶段 0 已覆盖 | 阶段 1 要补 |
| --- | --- | --- |
| 54 个静态路由 | 管理员（两实例 × 中英 × 深浅，216 次）、只读成员与运维人员（两实例中文深色，各 108 次） | 变更审批员、自定义角色（`custom-knowledge-reviewer`、D47 的 dev 限定账号）各跑一次 |
| 详情页与标签页 | 两实例：事件、告警组、调查画布与通用画布、对话、Runbook、服务、链路、Agent；nonprod：审阅页、执行详情 | prod 审阅页与执行详情（提案没有列表接口，对拍取不到 ID，需要从演练报告或审计取）；评测用例页（两实例都还没有执行过的评测，先造一个）；nonprod Agent 运行；通用画布详情 `GET /canvases/{id}`（现有样本全是调查画布） |
| 写入口的权限门控 | 只读成员、运维人员的门控检查无问题；管理员缺 `proposals.approve` 属设计 | 审阅页上的批准门控（只在 `/review/:id`），用非审批角色打开一次 |

### 1.3 数据状态（计划附录 B 的十类）

| 状态 | 阶段 0 情况 | 阶段 1 要补 |
| --- | --- | --- |
| 空 | 未专门测 | nonprod 的 dev、staging（无实验映射）各走一遍主要页面；新登记一个服务后看服务详情 |
| 部分来源不可用 | Loki/Tempo/Prometheus/ES/Alertmanager 停机、GitLab 暂停都通过 | K8s 采集失败超过 5 分钟后 `collection.state=stale` 与前端提示（`outage.py kubernetes` 加 `K8S_DOWN_SECONDS=330`） |
| 第一次 | 已在第一轮验收中遇到并修过（PA-104、PA-88、PA-70） | 新环境 dev/staging 的第一条告警、第一个提案（随“空”一起做） |
| 量大（只测功能） | PA-85 修过调查选择器 | 列表超过 20、100 条的分页（告警、事件、审计、对话）；节点很多的画布 |
| 不完整 | 缺父 Span 已有检查；null 字段在详情页对拍中查出 PA-109 | 其余详情页的 null 字段随 1.2 一起看 |
| 时间 | 会话 12 小时到期在登录用例里；凭据到期提醒在回归里 | 跨天（收件箱“今天/昨天”）、7 天链路保留边界、时区；阶段 2 跨天自然覆盖 |
| 多人与并发 | 两人批准、布局版本冲突已有检查 | 两人同时认领同一告警；两人同时改同一复盘 |
| 角色 | 见 1.2 | 撤权后旧会话（成员页撤权 → 旧会话下一请求 401/403） |
| 语言与主题 | 管理员对拍覆盖中英深浅 | 后端生成的文字（PA-113 一类）在英文界面下的表现 |
| 依赖慢或断 | 模型不可达、数据库停 20 秒、流式中途重启都通过；交付演练遇到过一次 `model_unavailable`（PA-107） | 模型返回慢（接近超时）时的提示 |

### 1.4 独立套件

阶段 0 已全部在 5502f631 上跑过，失败项修脚本后重跑通过。阶段 1 只补一项：

- 回滚反例“无写权限”（`rollback_edges.py write-forbidden`）：统一登记 P3/P4 之后，执行回滚需要已批准的写入目标，原来的前置条件已经过时。要先在统一登记下给 message-api 配一个实验账号无权 patch 的写入目标，再跑这个反例。

### 1.5 已知未完成的页面（WAN-125～128）

按计划 2.3（界面有入口、点了却失败算缺陷）处理，不按 4.2 归为缺能力，见 D48。阶段 1 先在实例上确认，确认后登记为 PA。

| Issue | 页面 | 代码现状（10-16 核对） |
| --- | --- | --- |
| WAN-125 | 从画布保存模板 | 仍默认勾选“只读取证步骤”“关联的 Runbook”（后端会 422），文案写“团队模板”；“复制为团队模板”只弹提示，不做任何事（同 PA-21） |
| WAN-128 | 从模板启动调查 | `POST /investigation-templates/{id}/start` 仍不带请求体，真实后端需要应用、版本和请求 ID |
| WAN-127 | 新建 Runbook | INC-1042 已改为只在示意数据模式出现；仍不传 `expected_source_sha256`，其余来源选择需实测 |
| WAN-126 | 对话列表 | 上下文用量已可空；列表的 `tokens` 仍声明为必有值 |

### 1.6 预先归类

- 豁免（`.local/acceptance/e2e/crawl-exemptions.json`）：服务韧性 `GET /services/{id}/resilience` 503 `resilience_source_unavailable`，按契约只有五类来源都配好才返回 200。
- 已登记缺陷：PA-105～113（PA-109 已修复）。复现时引用原编号，不重复登记。
- 契约 mock 或待实现：按契约预先归为缺来源或缺能力，不当成新缺陷。

### 1.7 阶段 1 的顺序

1. 核心闭环上的“首次验证”与 WAN-125/128：告警 → 调查 → 提案 → 批准 → 执行 → 复盘 → Runbook。
2. 页面与角色的补项（1.2）、数据状态的补项（1.3）。
3. 集成中心、平台设置、成员与角色上的“首次验证”。
4. Agent 运行台、记忆、知识源上的“首次验证”。
5. 62 条“已调用”写接口的六层补核对。

实例上的测试单线执行；写检查脚本和修缺陷可以并行（计划 4.2）。

## 第二部分：任务卡池（阶段 2）

每张卡写明角色、做什么、检查点。每轮按 3.2 的角色顺序，从该角色的卡里按编号顺序取，凑够 5～8 张。一个角色的卡取完就从头再来。

### 运维人员

| 编号 | 做什么 | 检查点 |
| --- | --- | --- |
| O1 | 在告警页或收件箱认领一个告警组，看策略与关联服务 | 认领状态刷新后仍在，审计能查到，其他人看到的是同一负责人 |
| O2 | 从事件发起调查，在画布上问 AI，派生一个分支提问，回写到根节点 | 刷新后节点回复仍在（PA-75），回写修订号加 1，证据引用能点开 |
| O3 | 新建对话，带 @服务 和一个文本附件提问；中途刷新页面 | 续传不重放、不丢消息，回答引用了附件内容 |
| O4 | 看指标看板、日志、链路（切 Errors/Slow 视图和时间范围），保存一个链路视图再删除 | 抽一个数字和 Prometheus/Loki/Tempo 实际值核对 |
| O5 | 从静默抽屉给进行中的调查提静默提案（先预览） | 提案进入审阅页，内容与预览一致 |
| O6 | 从画布上 AI 的回滚建议或服务详情发起回滚提案 | 目标修订正确，原因必填，提案进入审阅 |
| O7 | 宣告人工事件，记时间线，改状态到已解决，写复盘草稿并提交审核 | 时间线顺序与时间正确，复盘状态正确流转 |
| O8 | 从已发布复盘起草 Runbook，编辑步骤，试跑只读步骤，提交审核 | 来源和步骤与复盘对得上，试跑结果如实 |
| O9 | 在变更页看部署、流水线、合并请求，打开流水线详情 | 与 GitLab 实际值一致（可用 `gitlab_compare.py` 对照） |

### 变更审批员

| 编号 | 做什么 | 检查点 |
| --- | --- | --- |
| A1 | 用通行密钥批准一个待审提案，看执行时间线与验证结果 | 不能批准自己发起的提案；执行完成后结果卡（D37 方案 A）显示批准人 |
| A2 | 驳回一个提案并写理由 | 发起人能看到驳回与理由，提案不再执行 |
| A3 | 打开已批准、已驳回、已执行三种审阅页 | 右栏是结果卡，不再显示批准表单 |

### 只读成员

| 编号 | 做什么 | 检查点 |
| --- | --- | --- |
| V1 | 巡检首页、服务目录与详情、拓扑、链路 | 数字与运维人员看到的一致 |
| V2 | 尝试发起调查、新建对话、提静默 | 写入口禁用并提示所需权限（PA-64），没有点了才 403 的入口 |

### 平台管理员

| 编号 | 做什么 | 检查点 |
| --- | --- | --- |
| M1 | 邀请成员（邮件）、分配角色，再撤销角色 | 邮件可达，撤权后旧会话下一次请求被拒 |
| M2 | 集成中心：测试连接器，改一条服务映射再改回，看覆盖度和 Webhook 页 | 映射修订号与审计对得上，覆盖度如实 |
| M3 | 平台设置：改工作区设置、通知设置并还原；做一次模型检查 | 修改有 ETag 冲突保护，审计可查 |
| M4 | Agent 运行台：新建 Agent，加 Prompt 版本并试运行，建评测 | 配置不可变（改配置要新版本），无评测门槛的金丝雀被拒 |
| M5 | 上传一个知识源并同步，检索；在记忆页编辑一条事实 | 检索能命中刚上传的内容，记忆修改有审计 |
| M6 | 告警规则：AI 生成一条规则，预览、试跑、保存草稿，再走启用和停用 | 规则内容与仓库文件逐字一致，执行时间线完整 |
| M7 | 配置提升：nonprod 导出，实验侧合并，prod 导入 | 导入后对象落在 prod 自己的服务上 |
| M8 | 审计：按条件查今天的操作，导出 CSV | 今天各卡做过的写操作都查得到 |

### 自定义角色与账号

| 编号 | 做什么 | 检查点 |
| --- | --- | --- |
| C1 | 用 `custom-knowledge-reviewer`（approver2）发布复盘、批准 Runbook | 作者不能自己发布（`self_review_forbidden`） |
| C2 | 用 D47 的 dev 限定账号在 dev 写、在 test 只读 | dev 能写，test 的写入口禁用或被拒 |
| P1 | 任意角色：个人资料（头像、偏好、中英切换、深浅色）、TOTP 登记与删除、撤销其他会话 | 当前会话有标记（PA-69），撤销后被撤会话立即失效 |

## 第三部分：轮换表（阶段 2）

### 3.1 演练（每轮一个，按顺序循环）

1. 重试风暴 → 2. Pod OOM → 3. 坏发布与回滚 → 4. 消费者停止 → 5. D11 交付演练 → 6. 节点压力 → 7. 告警规则启用与停用 → 8. Pod 崩溃 → 9. 配置提升

相邻两项尽量不打同一个 Pod 或服务（阶段 0 的 Pod 崩溃演练就是被前一个 OOM 演练干扰的）。

### 3.2 角色

运维人员 → 变更审批员 → 只读成员 → 平台管理员 → 自定义角色与账号，然后循环。

### 3.3 数据状态（每轮取 1～2 个，挑当前版本还没跑过的）

时间 → 量大 → 部分来源不可用 → 多人与并发 → 不完整 → 空 → 第一次 → 角色 → 语言与主题 → 依赖慢或断

## 附录 A：写接口逐条清单

“阶段 0 调用来源”是在验收脚本或用例里找到直接调用，或在 16:26 之后的代理日志里出现过；不代表已按六层验证。

**me**（15）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `DELETE /me/avatar` | v1.1·已实现 | — | 阶段 1 首次验证 |
| `DELETE /me/mfa/totp/enrollment` | — | — | 阶段 1 首次验证 |
| `DELETE /me/mfa/totp` | — | — | 阶段 1 首次验证 |
| `DELETE /me/sessions/{id}` | implemented | auth.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `PATCH /me/preferences` | implemented | — | 阶段 1 首次验证 |
| `POST /me/mfa/totp/confirm` | — | — | 阶段 1 首次验证 |
| `POST /me/mfa/totp/enrollment` | — | — | 阶段 1 首次验证 |
| `POST /me/passkeys/deletion/finish` | — | — | 阶段 1 首次验证 |
| `POST /me/passkeys/deletion/options` | — | — | 阶段 1 首次验证 |
| `POST /me/passkeys/registration/finish` | — | golden-approval.spec.ts；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /me/passkeys/registration/options` | — | golden-approval.spec.ts；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `PUT /me/ai-preferences` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `PUT /me/avatar` | v1.1·已实现 | — | 阶段 1 首次验证 |
| `PUT /me/notification-settings` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `PUT /me/profile` | implemented | auth.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**alert-rules**（7）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /alert-rules/generate` | implemented | — | 阶段 1 首次验证 |
| `POST /alert-rules/import` | implemented | — | 阶段 1 首次验证 |
| `POST /alert-rules/preview` | implemented | — | 阶段 1 首次验证 |
| `POST /alert-rules/{id}/disable` | — | alert_rule_loop.py, d11_negatives.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /alert-rules/{id}/dry-run` | implemented | — | 阶段 1 首次验证 |
| `POST /alert-rules` | implemented | alert_rule_loop.py, config_sync.py, d11_negatives.py 等；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `PUT /alert-rules/{id}` | implemented | alert_rule_loop.py, d11_negatives.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**investigations**（5）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `PATCH /investigations/{id}/nodes/{id}` | implemented | delivery_drill.py, golden.py, rollback.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /investigations/{id}/branches` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /investigations/{id}/evidence` | — | — | 已登记 PA-105（V3 下 501） |
| `POST /investigations/{id}/nodes/{id}/messages` | implemented | delivery_drill.py, golden.py, rollback.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /investigations/{id}/writeback` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**incidents**（4）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /incidents/{id}/investigation` | — | delivery_drill.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /incidents/{id}/status` | — | config_sync.py, golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /incidents/{id}/timeline` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /incidents` | implemented | config_sync.py, d11_negatives.py, delivery_drill.py 等；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |

**memory**（4）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `PATCH /memory/facts/{id}` | v1.1·已实现 | — | 阶段 1 首次验证 |
| `POST /memory/conflicts/resolve` | implemented | — | 阶段 1 首次验证 |
| `POST /memory/facts/{id}/{id}` | implemented | — | 阶段 1 首次验证 |
| `PUT /memory/policy` | v1.1·已实现 | — | 阶段 1 首次验证 |

**proposals**（4）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /proposals/{id}/approve/options` | — | d11_negatives.py, rollback.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /proposals/{id}/approve` | implemented | d11_negatives.py, rollback.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /proposals/{id}/comments` | implemented | — | 阶段 1 首次验证 |
| `POST /proposals/{id}/reject` | implemented | inputs.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |

**agents**（3）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /agents/{id}/rollback` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /agents/{id}/rollout` | implemented | config_sync.py, golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /agents` | v1.1·已实现 | config_sync.py, detail-pages.spec.ts, golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**auth**（3）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /auth/invitations/accept` | — | accounts.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /auth/login` | — | auth.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /auth/logout` | — | auth.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**connectors**（3）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `PATCH /connectors/{id}` | implemented | gitlab_setup.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /connectors/{id}/test` | implemented | gitlab_setup.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /connectors` | implemented | gitlab_setup.py, golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**knowledge-sources**（3）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /knowledge-sources/registrations/{id}/sync` | — | — | 阶段 1 首次验证 |
| `POST /knowledge-sources/test` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /knowledge-sources` | implemented | faults.py, golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**members**（3）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /members/invitations` | implemented | accounts.py, golden-invite.spec.ts；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /members/mfa-reminders` | implemented | — | 阶段 1 首次验证 |
| `POST /members/sync` | implemented | — | 阶段 1 首次验证 |

**runbooks**（3）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /runbooks/{id}/dry-run` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /runbooks/{id}/submit` | implemented | config_sync.py, golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /runbooks` | implemented | config_sync.py, detail-pages.spec.ts, environments.py 等；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |

**service-mappings**（3）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /service-mappings/accept` | implemented | gitlab_setup.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /service-mappings/discover` | implemented | gitlab_setup.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `PUT /service-mappings` | implemented | environments.py, gitlab_setup.py, mapping_writes.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |

**silences**（3）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `DELETE /silences/{id}` | implemented | — | 阶段 1 首次验证 |
| `POST /silences/preview` | implemented | — | 阶段 1 首次验证 |
| `POST /silences` | implemented | detail-pages.spec.ts, drill.py, environments.py 等；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |

**agent-runs**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /agent-runs/{id}/annotations` | v1.1·已实现 | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /agent-runs/{id}/feedback` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**canvases**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /canvases/{id}/nodes/{id}/messages` | implemented | — | 阶段 1 首次验证 |
| `POST /canvases` | implemented | alert_rule_loop.py, config_sync.py, d11_negatives.py 等；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |

**conversations**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /conversations/{id}/messages` | implemented | ai_metrics_probe.py, d11_negatives.py, drill.py 等；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /conversations` | implemented | ai_metrics_probe.py, d11_negatives.py, detail-pages.spec.ts 等；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |

**executions**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /executions/{id}/recheck` | — | d11_negatives.py | 阶段 0 已调用，阶段 1 按六层补核对 |
| `POST /executions/{id}/report/publish` | implemented | — | 阶段 1 首次验证 |

**ingest**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /ingest/pull/{id}/test` | implemented | — | 阶段 1 首次验证 |
| `POST /ingest/push/test` | implemented | — | 阶段 1 首次验证 |

**inhibitions**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /inhibitions/preview` | implemented | — | 阶段 1 首次验证 |
| `POST /inhibitions` | implemented | faults.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**mcp-servers**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `PATCH /mcp-servers/{id}/tools/{id}` | implemented | — | 阶段 1 首次验证 |
| `POST /mcp-servers/k8s-mcp/review` | implemented | — | 阶段 1 首次验证 |

**notification-routes**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /notification-routes/test` | implemented | — | 阶段 1 首次验证 |
| `POST /notification-routes` | implemented | faults.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**prompts**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /prompts/{id}/trial` | implemented | — | 阶段 1 首次验证 |
| `POST /prompts/{id}/versions` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**services**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /services` | implemented | credentials.py, delivery_drill.py, detail-pages.spec.ts 等 | 阶段 0 已调用，阶段 1 按六层补核对 |
| `PUT /services/{id}/slo` | v1.1·已实现 | — | 阶段 1 首次验证 |

**trace-views**（2）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `DELETE /trace-views/{id}` | v1.1·已实现 | — | 阶段 1 首次验证 |
| `POST /trace-views` | v1.1·已实现 | traces_baseline.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**agent-tools**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /agent-tools` | v1.1·已实现 | — | 阶段 1 首次验证 |

**alert-groups**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /alert-groups/bulk` | implemented | — | 阶段 1 首次验证 |

**alerts**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /alerts/{id}/claim` | implemented | — | 阶段 1 首次验证 |

**api-tokens**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /api-tokens` | implemented | d11_negatives.py, golden.py, rollback.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**audit**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /audit/export` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**canvas-templates**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /canvas-templates` | implemented | config_sync.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**config-sync**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /config-sync/{id}/import` | implemented | config_sync.py（`mr-{iid}`，字面匹配未识别） | 阶段 0 已调用，阶段 1 按六层补核对 |

**eval-cases**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /eval-cases/{id}/labels` | implemented | — | 阶段 1 首次验证 |

**evals**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /evals` | implemented | detail-pages.spec.ts, golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**guardrails**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `PATCH /guardrails/{id}` | implemented | — | 阶段 1 首次验证 |

**investigation-templates**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /investigation-templates/{id}/start` | implemented | — | 阶段 1 首次验证 |

**knowledge**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /knowledge/search` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**postmortems**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /postmortems` | implemented | config_sync.py, detail-pages.spec.ts, golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**promotions**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /promotions` | implemented | config_sync.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**roles**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `PUT /roles/{id}` | implemented | accounts.py, config_sync.py, golden-invite.spec.ts 等 | 阶段 0 已调用，阶段 1 按六层补核对 |

**rollback-proposals**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /rollback-proposals` | v1.1·已实现 | delivery_drill.py, rollback.py, rollback_edges.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**security**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `PUT /security` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |

**uploads**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /uploads` | implemented | golden.py, inputs.py；代理日志 | 阶段 0 已调用，阶段 1 按六层补核对 |

**webhooks**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `POST /webhooks/{id}/redeliver` | mock | — | 已登记 PA-106（未注册，契约 mock） |

**workspace**（1）

| 接口 | 契约状态 | 阶段 0 调用来源 | 阶段 1 处理 |
| --- | --- | --- | --- |
| `PUT /workspace` | implemented | golden.py | 阶段 0 已调用，阶段 1 按六层补核对 |
