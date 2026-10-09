# WAN-119 本人资料证据接线

`GET/PUT /api/v1/me/profile` 的完整 `ProfileView` 需要本实例、当前用户及所选环境的权威证据。账户数据库只证明已保存的姓名、部门、职务、时区、联系方式、通知偏好、会话 ID/到期时间和已有密码修改审计；它不能证明联系方式已验证、MFA 状态与因子，也不记录会话的设备和登录方式。未配置或未取得完整证据时，两条路由返回 `503 identity_evidence_unavailable`，PUT 不写入或记录成功审计。现有会话删除路由独立运行。

## 接口与信任边界

启动时在 `v3settings.Service` 上设置 `ProfileInstanceID` 和 `ProfileEvidence`。`ProfileInstanceID` 必须等于现有 `MeEvidence.MFAInstanceID`，同时需要现有 `MeEvidence.PasskeyRPID`；事务还会把外部因子种类及数量与本实例 `/me` 使用的 TOTP/passkey 登记逐项核对。内置 `NewHTTPProfileEvidenceSource(endpoint, bearerToken)` 是可注入的客户端，接受受信 HTTPS 地址和独立 Bearer 凭据；它不会自行发现设备、认证方式或策略。连接超时为 3 秒，不跟随重定向，最多读取 64 KiB 响应；错误不回显 URL、凭据或源响应。

正式入口通过 `AIOPS_V3_PROFILE_EVIDENCE_URL` 与 `AIOPS_V3_PROFILE_EVIDENCE_TOKEN_FILE` 成对显式启用；凭据文件须为绝对路径、0600 普通文件，令牌不入库。两项都不设时返回本实例事实的部分资料（见下节“未配置身份来源”），只设置一项或配置无效时启动失败。部署者必须先核实 HTTPS 来源确为当前实例和租户的权威服务，并能覆盖全部活动会话；仅填写 URL 与令牌并不能证明这些事实。

客户端向权威服务发送一次 `POST` JSON：`instance_id`、`user_id`、`environment_id`、`current_session_id`、`contacts: [{kind,value}]` 与 `session_ids`。会话 ID 是数据库保存的 SHA-256 令牌哈希，不发送原始 Cookie。权威服务必须返回同名的精确绑定，`complete: true`、`observed_at`、`valid_until`、`contacts: [{kind,value,verified,label?,note?}]`、`mfa_enabled`、`org_rule`、`factors: [{kind,name,sub}]` 与 `sessions: [{id,device,login_method}]`。`verified` 与 `mfa_enabled` 即使为 `false` 也必须显式出现；数组可为空但不可缺失。请求中的邮箱和电话必须逐项且仅出现一次，响应还可包含一个由该权威服务绑定到同一用户的飞书联系方式。所有有效会话必须逐项覆盖，登录方式只接受 `password`、`oidc` 或 `passkey`；因子类型只接受 `totp`、`passkey`、`recovery`。观察时间最多落后 60 秒、最多超前 10 秒，且有效期不得超过观察后 5 分钟。

`mfa_enabled` 表示权威服务观察到活动 TOTP 或 passkey 因子，**不证明登录时强制 MFA，也不产生操作审批保证**。`org_rule` 须由策略权威服务提供真实文案。`password_changed` 没有审计时明确显示未记录；`tokens` 显示个人令牌尚未接入，不把这些字段伪装为可编辑能力。接线前应核实权威服务能够覆盖当前实例的所有活动会话；现有登录路径本身没有保存设备或登录方式。

## 请求执行

服务端先取得当前账号联系方式与活动会话 ID，在任何账户/授权数据库锁之外读取外部证据。之后读取或写入事务按既有全局授权锁、账号行、当前 Cookie 会话行的顺序加锁，复核所选单一环境的 `environment.view`、完整联系方式/会话集合和证据有效期。GET 返回此事务的资料快照。PUT 仅允许更改 `name`、`display_name`、`title`、`timezone` 和 `notifications`；提交的 `department`、`contacts` 和完整 `security` 必须等于当前快照，写入与不含证据/凭据的 `v3.profile.updated` 审计一起提交。外部服务不在数据库锁内调用。

本批次无新数据库迁移。WAN-117 集成层仍需选择真实权威服务、保护 Bearer 凭据、显式注入上述两个字段并验证生产来源；主契约和冻结页面的状态由集成批次另行判断。

## 未配置身份来源（2026-10-09 生产验收 PA-16）

按已确认的“未知状态”方案，不配置身份来源时不再整页 503。`GET` 返回 `partial: true`：姓名、部门、职务、时区、联系方式、通知、会话 ID 与到期时间、密码修改审计来自账户数据库；`mfa_enabled` 和 `factors` 只反映本实例自己的 TOTP/通行密钥登记。`contacts[].verified`、`security.org_rule`、`security.sessions[].device` 为 `null`，登录方式不出现在会话说明里，这四项列入 `unavailable_fields`。`PUT` 在此模式下同样只改可编辑字段，其余字段（包括 `null`）必须与当前视图一致，伪造“已验证”返回 422。已配置来源但来源失败、过期或不全时仍返回 503，不退回部分资料。
