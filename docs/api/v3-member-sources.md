# V3 成员来源与 MFA 提醒配置

正式进程可选用 `AIOPS_V3_MEMBER_SOURCES_CONFIG` 指向一个**绝对路径、普通文件、权限 0600、最大 32 KiB** 的严格 JSON 对象。未设置时三个来源均为 `nil`，相关请求保持明确不可用；文件存在但字段缺失、未知或与当前 V3 实例不一致时启动失败。启动只读取配置和凭据文件，不调用飞书或 MFA 服务，不发送消息。

配置仅绑定一个 V3 实例环境。`instance_id` 必须与 `AIOPS_V3_INSTANCE_CONFIG` 的 `domain` 完全相同，`tier` 和 `environment_id` 也必须与该实例精确匹配。`tenant_binding_id` 是部署人员审定的稳定租户命名空间，不可在同一租户下任意更换；允许 1–40 位 ASCII 字母、数字、下划线或连字符。配置可只启用目录或只启用 MFA 证据；启用提醒必须同时配置 MFA 证据。

占位结构（不要把真实凭据或此示例直接提交）：

```json
{
  "tier": "nonprod",
  "instance_id": "aiops.example.invalid",
  "environment_id": "dev",
  "tenant_binding_id": "reviewed-tenant-a",
  "directory": {
    "app_id": "cli_REPLACE",
    "app_secret_file": "/private/path/directory-app-secret",
    "root_department_id": "0",
    "address_book_scope_approved": true
  },
  "mfa_evidence": {
    "url": "https://identity.example.invalid/v1/aiops/mfa-snapshot",
    "bearer_token_file": "/private/path/mfa-evidence-token"
  },
  "mfa_reminders": {
    "app_id": "cli_SEPARATE_BOT",
    "app_secret_file": "/private/path/reminder-bot-secret",
    "tenant_scope_approved": true
  }
}
```

所有凭据文件都必须是绝对路径的 0600 普通文件，长度 1–4096 字节；只接受无内部空白的单个令牌/密钥值。错误不回显路径或密钥。`directory` 的 `root_department_id` 必须为 `0`，且 `address_book_scope_approved` 只应在管理员核实该飞书应用能读完整通讯录后设置。目录读取器有页数、部门和成员上界；任一页失败或不完整就拒绝同步。同步不会根据快照缺席自动停用用户，不能将其视为离职权威来源。一个进程配置只支持一个目录环境。

MFA 证据 URL 必须是经审核的 HTTPS DNS 地址，无显式端口、凭据、查询或片段；服务端不跟随重定向。正式入口使用绑定协议：`POST` JSON 请求包含精确的 `instance_id`、`tenant_binding_id`、`environment_id` 和 `user_ids`；响应必须是单个 JSON 对象，包含相同三个绑定字段与 `snapshot`。字段不匹配、环境越界、成员集合不完整、数据超过五分钟或值不可信时拒绝。上游必须对这个实例和租户拥有权威 MFA 登记及飞书 open ID 绑定，不能只转发本产品本地 TOTP 表或按邮箱猜 open ID。绑定字段的回显提供运行时交叉校验；**它不独立证明上游实际拥有该租户数据**，仍需部署方审核来源及其认证配置。

`mfa_reminders` 应是与目录读取应用不同的专用飞书机器人应用，`tenant_scope_approved` 只应在部署方核实同租户、已发布且具备向目标成员发私信权限后设置。当前飞书令牌协议没有可核验的租户回执，因此配置审核不能替代真实租户验证。提醒 API 会先取完整、鲜活、精确的 MFA 证据，限制每位收件人 24 小时内重复发送、每位管理员每小时最多 100 条及单次最多 20 条，并把每人的发送尝试持久记录。部分成员已发送而后续成员失败时，返回的错误不能解释为全未发送；未知结果保留待人工核对，不自动重试。飞书返回的 message ID 仅证明 API 接受，不证明用户已阅读。

配置可构造真实适配器，但不证明飞书权限、上游覆盖、迁移已执行、真实发送、前端适配或端到端验收。不要在尚未审定来源时放入真实配置，也不要把生产凭据写入仓库。
