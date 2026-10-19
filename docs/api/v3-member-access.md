# V3 成员授权入口（WAN-237）

范围：后端创建自定义业务角色、明确环境范围、分配成员直接角色及启停。复用 `rbac_roles`、`v3_role_grants`、`rbac_user_roles`；无需迁移。前端、目录认证接入、口令初始化、外部通知不在本次范围。

## 角色创建

`POST /api/v1/roles`，Cookie 会话与单一 `X-AIOps-Environment`。需要当前活跃人类账户拥有 **global roles.manage + global environment.view**，事务内重新校验。

```json
{
  "id": "custom-dev-reader",
  "name": "开发环境只读",
  "description": "批准的开发环境访问",
  "scopes": [{"kind":"environment","id":"dev"}],
  "matrix": [{"resource":"conversation","grants":[1,0,0,0,0]}]
}
```

示例的 matrix 必须补齐现有 19 个资源（PA-231 补了总览、链路、日志、模型、工作区设置；PA-232 把“对话与画布”拆成“对话”和末尾的“画布”），每个资源 5 个操作（view/change/delete/approve/manage）；使用既有角色矩阵资源码。服务同时创建相同范围的 `environment.view`，确保角色不是无环境入口的空壳。审批转换为 passkey 条件授权，仍受既有审批策略限制。

初版仅接受一个范围：所选具体环境，或请求中明确的 `global:*`。不会把所选环境暗中解释为全局；`all` 写入继续统一 409。角色固定为非内置 business，不写 legacy permission，也不创建 machine/system 角色。

返回 `201 {id,created:true,kind,scopes,matrix_revision:"1"}`；同 ID、名称、描述及规范化授权重复返回 200/created:false，不重复审计。内容冲突或同名不同 ID 返回 409。角色 ID 为 `custom-` 开头、8–64 位小写字母/数字/下划线/短横线，前缀后首位须为字母或数字。

既有角色矩阵 GET 可读取新角色。全局角色继续由原 PUT 修改；**原摘要矩阵仍拒绝编辑环境范围角色**，本轮不新增完整范围编辑器。可创建经审核的新角色，并显式替换成员的直接分配。

## 成员访问快照与更新

- `GET /api/v1/members/{id}/access`
- `PUT /api/v1/members/{id}/access`

两个入口均要求活跃人类拥有 **global roles.manage + global members.manage + global environment.view**。members.manage 单独不能授权；角色管理也不能单独修改成员。所选环境用于准入；返回的是该成员**全部环境**的角色与授权，避免把全局角色展示成仅当前环境生效。

GET 包含 `enabled`、`identity_kind`、`source`、`local_login_ready`、`roles[]`、`revision`。每个 role 有 id/name/kind/origin/matrix_revision/grants；origin 为 direct、legacy、department。最后两种均明确为继承，不能通过此入口删除。SHA256 revision 覆盖完整快照，包括角色版本和实际授权；联系人修改不假装成权限版本。

```json
{
  "expected_revision": "GET 返回的 64 位小写十六进制 revision",
  "role_ids": ["custom-dev-reader"],
  "role_revisions": {"custom-dev-reader":"1"},
  "enabled": true
}
```

PUT 完整替换 **direct** 角色。空数组表示清除直接分配；若仍启用则必须保有直接或继承的环境准入，否则 422，调用方可以同时停用。每个提交角色必须附当前矩阵版本，可由创建响应或角色 GET 的 `X-Role-Matrix-Revision` 获取。任一成员快照/角色版本过期均返回 409/access_changed。

授予遗留权限角色或修改遗留特权身份还要求实际 `rbac_platform_administrators` 成员身份，不能以 V3 管理授权绕过旧平台管理边界。机器账户和机器角色不通过此入口管理。拒绝自停用；撤掉最后可登录平台管理员或最后可登录人类全局角色管理者返回 409。继承权限会参与这些判断。

授权更新撤销目标已有会话，须重新登录；完全一致的更新不产生新审计或撤销会话。重复使用旧版本仍返回 409，应重新 GET 确认结果，不能把旧请求无条件覆盖新权限。

## 邀请与目录成员的实际使用

1. 邀请接受流程创建已有本地凭证的 viewer 账户，初始无环境准入。
2. 管理员创建角色、读取成员访问快照、分配角色并启用。
3. 成员重新登录后按该角色范围访问；其他环境继续拒绝。

同步 Feishu/LDAP 成员可以同样获得角色并启用，但 `local_login_ready:false` 明确没有本地凭证。**授权/启用不建立组织 SSO，也不生成本地口令**；真实登录仍依赖独立配置、验证的身份认证链。请求携带 `initial_password` 等未知字段会被拒绝。

## 一致性与验证

事务锁顺序保持 `752018992 → 752018995 → 752018998`，与账户、legacy 分配及矩阵撤权写者一致。等待后校验原始 Cookie，重新读取真实人类身份和全部管理准入；写入、最后管理员保护和 `management_audit` 在同一事务。审核错误、存储失败、会话撤销全部回滚。会话撤销在最后校验之后、提交之前执行，允许合法自降权。

新增测试覆盖请求歧义/未知字段、显式范围、角色幂等、真实 PG 邀请接受与登录后授权、跨环境拒绝、旧会话撤销、CAS、角色版本、审计失败回滚、成员管理不能替代角色管理、继承角色及末位管理者保护、目录成员激活不伪造认证、等待身份锁期间原 Cookie 或权限撤销。PG 测试仅使用 `TEST_DATABASE_URL` 下随机 schema，完整迁移后清理；不发送外部邮件。
