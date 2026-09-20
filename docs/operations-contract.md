# 告警、通知与有界自动化

源码入口：`cmd/aiops/operations.go`、`internal/operations/`。Go 服务启动时加载配置、迁移业务表并启动随进程取消的调度循环。告警、调度、预算、租约、任务结果和通知 outbox 均保存在 PostgreSQL。该模块只读取证，不授予基础设施写权限。

## 连接告警与飞书

设置 `AIOPS_OPERATIONS_CONFIG=/path/to/operations.json`。未配置时没有外部告警源或飞书渠道；自动模型功能仍默认关闭。配置文件示例仅含占位路径和已登记资源的示意 ID：

```json
{
  "sources": [
    {
      "id": "primary-alertmanager",
      "token_file": "/run/secrets/alertmanager-ingress-token",
      "mappings": [
        {
          "labels": {"application": "checkout", "environment": "production"},
          "application_id": "<registered-application-id>",
          "environment_id": "<registered-environment-id>"
        }
      ]
    }
  ],
  "feishu_channels": [
    {
      "id": "production-operations",
      "application_id": "<registered-application-id>",
      "environment_id": "<registered-environment-id>",
      "webhook_file": "/run/secrets/feishu-webhook-url",
      "secret_file": "/run/secrets/feishu-signing-secret"
    }
  ],
  "public_url": "https://aiops.example.com"
}
```

配置由服务端管理，修改源或渠道映射后重启服务；凭据文件在实际请求时读取。令牌文件内容为至少 24 字符的独立入站令牌；将 Alertmanager webhook 指向 `POST /api/v1/webhooks/alertmanager/primary-alertmanager`，携带 `Authorization: Bearer <token>`。该入口不使用浏览器登录 Cookie。Alertmanager 继续负责告警规则、分组和路由，本平台接收其 `alerts` 数组。

映射要求所有登记标签精确匹配，并且唯一命中有效应用／环境。零命中、多重命中或失效绑定进入未分配异常队列，仅管理员可见；不会从任意告警字段猜测应用权限。

飞书 `webhook_file` 存完整机器人 webhook URL；只接受 HTTPS 的 `open.feishu.cn` 或 `open.larksuite.com` 的 `/open-apis/bot/v2/hook/` 路径，不跟随重定向。启用机器人签名时填写 `secret_file`，否则可省略。`public_url` 用于通知中的工作台／调查深链接，有飞书渠道时必须提供。凭据不放在 JSON 正文、前端或仓库中。配置有效渠道后，匹配的告警和领域事件会发送通知；关闭自动模型任务不会关闭这些通知。

## 状态、防重与交付语义

- 告警身份是 `source_id + fingerprint + startsAt`；相同正文重试不重复登记 occurrence。晚到的 firing 不能重新打开同次已 resolved 告警；新的 startsAt 表示新一次告警。
- 同一异常复用关联调查。告警 resolved、调查关闭、执行成功和业务恢复验证分别保存，不能互相替代。
- 站内异常和任务首先落库；飞书发送失败不阻塞它们。通知以 `channel + event_key` 防止重复入队，但远端 webhook 无幂等保证，投递语义是**至少一次**；超时后重试可能产生重复消息。
- 通知采用租约、递增退避和最多 5 次投递；失败状态可在通知列表看到。相同告警状态只对应一个事件键；AI 巡检对同一活动异常按 UTC 小时合并通知键。当前没有通用的可配置 30 分钟通知冷却功能。
- 通知仅含简短信息与页面链接，没有群聊审批或执行入口。恢复动作仍经过平台人工审批和独立执行器。

## 开启自动化

`GET /api/v1/automation/policies/{id}` 读取策略；`id` 为环境 ID，或管理员使用 `workspace`。管理员通过同路径 `PUT` 修改，正文包含完整策略和 `expected_version`；版本变化返回 409。先读取当前版本，再保存，例如：

```json
{
  "expected_version": 0,
  "inherit": false,
  "auto_initial": false,
  "patrol": false,
  "generate_experience_draft": false,
  "model_id": "<configured-model-id>",
  "actor_id": "<authorized-operator-user-id>",
  "interval_minutes": 30,
  "max_rounds": 4,
  "max_tool_calls": 8,
  "timeout_seconds": 120,
  "daily_budget": 48
}
```

示例 `expected_version=0` 仅适用于还没有策略记录的情况。环境没有独立策略或 `inherit=true` 时采用 workspace 策略；workspace 自身不能继承。自动初查、定时巡检、成功关闭后生成经验草稿是独立开关，默认均为 false；人工发起调查始终按用户权限操作。启用任一后台模型能力，必须指定已配置模型和有效操作者；该操作者仍受应用 ACL 约束。

默认巡检间隔 30 分钟、推理调用 4 次、工具调用 8 次、任务超时 120 秒、每环境每天 48 个后台任务。允许设置范围分别为 5–1440 分钟、1–8 次、1–16 次、15–300 秒、1–288 个任务。每日预算按 UTC 日期统计**已准入任务数**，不是 token 或费用；失败任务也消耗名额。自动初查、巡检和自动经验生成共用环境预算和固定并发限制：同一环境 1 个、所有进程合计 2 个。人工会话不计入这份后台任务预算。

## 执行、恢复与数据有效性

调度保存各环境 `next_due`，从当前时间计算下一次时间，不重放停机期间错过的巡检。自动初查使用正常调查 Worker，持久请求键解决重复触发和入队／关联之间的崩溃窗口。执行前以及工具调用、任务监控阶段重新检查当前策略、应用权限、环境绑定版本和租约；失效会停止或暂停任务。

初查的摘要和推理共用绝对截止时间，摘要调用扣减推理预算。Worker 中断后的自动初查 `Attempt > 1` 在模型调用前拒绝，状态记为 `indeterminate`，不能重新领取一份轮次／工具预算。人工会话原有重试行为不变。过期巡检不自动重放工具；保留已提交部分证据并标记无法判定。暂停、预算耗尽和失败任务在任务列表可见，当前没有通用重试／恢复按钮；操作者可检查证据后显式发起调查。

巡检只使用已登记的 `query_metrics`、`query_logs` 和 `search_runbooks`。模型输出仅允许 `clear`、`suspected`、`indeterminate`；疑似异常进入带 AI 标记的收件箱，证据保存在任务 `result`。正常观察不创建调查，Runbook 引用不能当作实时指标。

所有**已查询**的指标／日志响应都必须完整、非空且无错误；任一路缺数据、出现 NaN、缺时间戳或 `limit_reached`，都不能被另一条有效日志“补成正常”。当前保守新鲜度规则要求：HTTP 成功、采集时间与窗口末尾在最近 5 分钟内；每个指标序列都有合法数值及窗口内时间戳，最新样本距窗口末尾不超过 `max(2 分钟, 2 × step)`（上限 5 分钟）；日志有窗口内时间戳且至少一条在最近 5 分钟内。稀疏日志或低频指标可能因此得到 `indeterminate`，这不是业务健康结论。

当前巡检是有界证据分析，没有学习季节性基线、全量异常检测或自动生产修复。`clear` 只表示这次已查询范围未发现疑点，不代表所有指标、所有依赖都已覆盖。

## 查看与排障

`GET /api/v1/dashboard` 汇总关注项；`/incidents` 查看异常；`/automation/runs?environment_id=...` 查看任务状态及结果；`/notifications` 查看投递状态。使用 `POST /automation/runs/{id}/cancel` 取消活动任务。接口路径均带 `/api/v1` 前缀，并检查应用权限。

没有自动结果时依次核对：策略开关与继承、模型登记、actor 的应用权限、环境与连接器绑定、每日预算、任务错误和数据新鲜度。不要把无告警、无采样或连接器不可达解释为正常。
