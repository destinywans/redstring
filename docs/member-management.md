# 成员管理与企业目录导入

对应 WAN-80，2026-09-20 用户明确要求成员、团队、审计与集成分开。本轮为源码修改，按用户要求未构建、测试、运行迁移或浏览器验证。

## 产品入口

- 成员：独立成员目录、搜索筛选、详情、新建和编辑、停用/恢复、删除。
- 团队：独立团队与成员归属管理，继续使用单层团队。
- 审计：独立查看管理事件；不会混入模型或连接器配置。
- 集成：模型服务、连接器和知识向量服务。
- 工作区设置：现有后台自动化策略。组织管理不再作为集成的子菜单。

角色继续为 admin/operator/viewer。管理入口与服务端均要求管理员；应用所有权不会替代应用授权。

## 成员生命周期

`GET/POST /api/v1/users`、`GET/PATCH/DELETE /api/v1/users/{id}` 提供管理操作；管理响应在核心身份字段之外包含 department、job_title、source、external_id、deleted_at。本地成员由平台维护，企业导入成员保留目录来源与外部稳定 ID。

删除采用保留历史身份的方式：账号删除后不能继续登录，已有调查、批准人和审计引用保留。停用/角色/凭据变化撤销已有会话；最后一个启用的管理员不能被删除、停用或降权。列表默认不含已删除身份，可显式包含历史记录。

## IAM 导入边界

本轮支持管理员上传或粘贴企业目录导出的 JSON/CSV，并在页面查看预览后确认。不是实时 IAM 同步，也不提供 SSO；没有具体供应商、协议或凭据时不假称已接入企业 IAM。不会从用户填写的 URL 拉取目录。

导入行字段：`external_id,email,name,department,job_title`，前三项必填；每批最多 200 行。另填 `source` 作为企业目录标识。示例（仅占位数据）：

```csv
external_id,email,name,department,job_title
employee-001,member@example.invalid,示例成员,平台工程,工程师
```

- `POST /users/import/preview`：`{source,rows}` → fingerprint、逐行 create/update/conflict 和 can_import。
- `POST /users/import`：同一份 source/rows 加 fingerprint，重新核对目录及冲突后原子写入；变化后需要重新预览。
- `(source,external_id)` 用作稳定身份。邮箱碰撞本地账号或另一个身份时不自动接管。
- 新导入成员为停用的 viewer，没有可登录密码；管理员后续设置密码并启用，显式授予应用范围。
- 更新导入资料不提升角色、不覆盖应用授权，不从文件导入密码。目录成员的邮箱只能通过目录导入更新；姓名、部门、职位可在平台编辑，但后续导入会覆盖为目录提供的值。

迁移 `010_members.sql` 尚未运行。具体响应以 OpenAPI 为准，当前实现仍待后续授权验证。
