# V3 通用 Push 入站

此配置示例见 [v3-push-sources.json](v3-push-sources.json)。部署者将来源 ID、供应商 `generic`、**已持久化**应用 ID、当前 V3 环境 ID、可接收类型与两个私有文件路径固定在服务端；最多配置 50 个来源。来源 ID 在首个已签名请求处理时持久绑定原应用、环境和供应商，不能改绑；更换目标时须使用新 ID。JSON 和仓库里只有占位符；Bearer 令牌与 HMAC 密钥放在进程可读、权限仅对所属用户开放的普通文件中，不提交到 Git。启动时校验全部来源的文件、权限和凭据唯一性，接收时重新读取以支持轮换。`rate_per_minute` 是单来源每分钟上限，缺省 120、最高 600；`public_host` 是可选的公开主机名或 `host:port`。未配置时 GET 的 `endpoint.host` 为空，不回退到请求的 Host 头。GET 的 `configured_unverified` 只表示来源和公开主机已配置，不证明轮换后的文件、网络或发送方可用。

接收端点为 `POST /api/v1/events/{alert|change|incident|resource|oncall}`。每次请求必须发送：

| 请求头 | 值 |
| --- | --- |
| `X-AIOps-Source` | 服务端配置的来源 ID |
| `Authorization` | `Bearer <token>`，内容来自该来源的令牌文件 |
| `Idempotency-Key` | 发送方为这一事件生成的稳定唯一键 |
| `X-Timestamp` | 发送时的 Unix 秒数 |
| `X-Signature` | `sha256=` 加小写十六进制 HMAC-SHA256 |

签名输入是 **X-Timestamp 原样字符串、LF、X-AIOps-Source 原样字符串、LF、路径事件类型、LF、Idempotency-Key 原样字符串、LF、JSON 正文原始字节**的拼接。LF 是单个 `0x0A` 字节，正文后不再补 LF；前四个字段均不允许 LF。密钥来自单独的 `hmac_secret_file`，不同来源不得共用 Bearer 令牌或 HMAC 密钥，即使文件路径不同。服务端按最多五分钟时钟偏差验证，并在读取有界正文前先验证来源与 Bearer；来源在配置中的应用和环境是权威范围，正文不能选择别的服务或环境。相同来源、事件类型和幂等键在 24 小时窗口内重复提交返回 409；规范事件 ID 持续保持唯一。超过来源速率上限返回 429，附 `Retry-After: 60`。

下例是 `change` 的规范事件；五种类型都使用与路径一致的 `<type>.v1` schema。正文上限为 64 KiB，`attributes` 最多 16 个有界字符串键值，凭据类键名被拒绝；事件属性只保存在受限的规范事件记录，不进入页面投递摘要或管理审计。

| 类型 | 可用 `state` | 额外要求 |
| --- | --- | --- |
| `alert` | `firing`、`resolved` | `severity` 必须是 `critical`、`warning` 或 `info` |
| `change` | `started`、`succeeded`、`failed`、`rolled_back` | 不接收 `severity` |
| `incident` | `open`、`acknowledged`、`resolved` | 不接收 `severity` |
| `resource` | `created`、`updated`、`deleted` | 不接收 `severity` |
| `oncall` | `started`、`ended`、`handoff` | 不接收 `severity` |

```json
{
  "schema": "change.v1",
  "event_id": "deploy-812",
  "occurred_at": "2026-09-29T12:00:00Z",
  "subject": "checkout-api",
  "title": "Deploy v2.8.1",
  "state": "succeeded",
  "attributes": {
    "version": "v2.8.1",
    "commit": "a1f9c2e"
  }
}
```

202 表示规范事件、接收记录、幂等占用和审计已持久提交，不表示已经生成产品事故或向外投递。GET `/api/v1/ingest/push` 的计数与最近接收记录来自当前环境内可见的持久数据；本机测试事件标记为 synthetic，不计入真实事件数。没有可用来源时返回真实空值，配套栈不声称已部署。POST `/api/v1/ingest/push/test` 需要当前环境的管理权限和 `{ "type": "change" }`，在唯一可选来源上生成并持久化一条本机测试事件；它不证明外部发送方已配好令牌、签名或网络连通。

当前没有获批的数据保留期，也没有清理调度。幂等键仅在相同键再次到达且其 24 小时窗口已过时替换；未再次使用的键、速率窗口、接收记录和规范事件会持续保留，规范事件 ID 因此始终保持唯一。模块的来源限流只约束通过来源认证和签名的请求，不能替代入口层对无效令牌请求的限流。上线前应单独确认留存期限、清理授权、入口层限流和容量预算；管理审计遵循其自身保留策略。

恢复时先检查来源是否绑定仍有效的应用与环境，再检查私有令牌/HMAC 文件是否可读及发送端时钟偏差；错误修复后以新的幂等键重试。来源配置变化需更新部署配置；不要把令牌、签名、完整事件正文或文件内容贴入工单或日志。冻结前端目前省略测试请求的 `type`，复制的 curl 也缺来源、时间戳及签名头；这些交互仍待单独适配。
