# 独立设置工作区契约

设置承载平台资料、成员、部门和角色 / 应用范围。目录来自 PostgreSQL；内置角色及应用 grants 是实际权限来源，部门负责人只是组织资料，不赋予额外权限。全局环境筛选不改变平台身份目录。

## 2026-09-21 部门产品语义修订（WAN-103）

用户确认产品只有部门，成员与部门页面不展示 Mock/示例标签或说明。部门列表直接展示名称、负责人、成员数、关联应用、描述和更新时间，无目录侧栏、成员数筛选、操作列或删除入口；名称打开覆盖式详情，新建与编辑沿用现有接口。

成员归属以 `members.team_ids -> teams` 的成员关系为准，页面展示“部门”；修改归属在部门成员卡片完成，成员表单不再编辑旧自由文本 `users.department`。`teams`、`team_members`、`subject_type=team` 等稳定内部字段保留兼容，其产品含义统一为部门。旧 `users.department`、`settings_team_profiles.department` 和聚合 `departments` 仅保留兼容资料，不再作为第二套组织目录。IAM 导入仍沿用原接口，不声称已自动把外部自由文本同步为组织成员关系。

API 的 `is_mock/source/mock_notice` 与数据库禁止示例登录约束保留；隐藏来源文案不启用账号或改变授权。页面显示真实“已停用”状态。`normalize-settings-departments.py` 仅修正明确标记的三条种子记录的原始名称和描述，逐字段精确匹配，人工修改保留；支持 dry-run，重复运行无变更。其他资源页的示意数据标记不受此调整影响。

以下兼容 API 的团队术语对应上述部门；历史种子机制不变。

## 注册与迁移

- 启动沿用 `Store.Migrate`，自动执行 `migrations/012_settings.sql`；无需单独模块迁移。
- 在主程序初始化用户 / bootstrap 后调用 `settings.New(db, "dev").Register(api)`。版本传真实构建标识；没有发行版本时显示 `dev`，不使用概念图版本号。
- API 在请求时读取 `api.Runtime.Models()`、`api.Integrations` 和可选 `ai_extensions` MCP 记录；只统计已注册配置，`configured_unverified` 不代表连接健康。
- OpenAPI 生成器在定义 `Member` 后执行 `exec(Path('scripts/settings-openapi.py').read_text())`。

## API

| 路径 | 权限 | 内容 |
| --- | --- | --- |
| `GET /api/v1/settings/overview` | viewer | 平台资料、版本、真实配置数量、管理权限与能力边界 |
| `PATCH /api/v1/settings/overview` | admin | 保存基本资料，携带最新 revision；返回保存后的 workspace |
| `GET /api/v1/settings/directory` | admin | 成员、团队、部门聚合、固定角色、应用与 grants |
| `PATCH /api/v1/settings/teams/{id}/profile` | admin | 保存团队负责人 / 部门，返回 TeamProfile |

`overview` 返回 `{workspace,version,integrations,can_manage,capabilities}`。workspace 字段为 `id,name,description,owner_id,owner_name,deployment_mode,timezone,language,revision,created_at,updated_at`。owner 在迁移时选择现有启用真实管理员；若迁移早于 Bootstrap 且 owner_id 为空，则读取时回退到有创建审计最早的启用真实管理员（无审计时按 ID 稳定选择）；ID 与姓名取同一记录，完全无候选时为空，不写回或虚构所有者。创建时间指设置资料建立时间，不是推测的历史安装日期。部署方式为 `self_hosted`。迁移不修改已有真实账号。

PATCH 接受 `{name?,description?,timezone?,language?,revision}`。名称去前后空格后 1–120 个 Unicode 字符，简介不超过 200 字符；timezone 为有效 IANA 时区；language 目前仅 `zh-CN`。这些是平台默认偏好，不承诺尚不存在的翻译或通知功能。owner、部署方式、版本、时间不可写，未知字段拒绝。旧 revision 返回 409；更新和管理审计同事务提交，审计包含保存后的简介，简介单独修改也可追踪。

`integrations` 每项 `{kind,total,configured,state}`，kind 为 model / mcp / connector / embedding（后者汇总 embedding、retrieval、vector 注册项）。MCP 仅管理员可看统计，其他角色 `state=restricted`；模块表未安装时 `unavailable`，无配置时 `not_configured`。不暴露凭据、网络地址或文件路径。`capabilities={custom_roles:false,resource_acl:false,team_owners:true}`。

`directory` 字段：

- `members`：原 Member 字段 + `is_mock`、`team_ids`、可空 `created_at`。原字段为 id/email/name/role/disabled/department/job_title/source/external_id/deleted_at；不含密码。包含已软删除记录供只读筛选；角色和部门统计排除已删除成员。created_at 仅来自该用户的 account.created 审计；namespaced Mock 成员使用首次 settings.demo.seeded 审计时间。批量导入仅有总数审计、无法对应个体时保持 null，不推测日期。
- `teams`：id/name/description + owner_id/owner_name/department/is_mock/created_at/updated_at/member_ids/applications。无扩展资料时负责人 / 部门为空，时间为 null。时间是扩展资料时间，不假称团队所有属性的最近修改时间。
- 团队 `applications` 项为 `{id,name,key,permission,relationship}`。relationship 为 `owner` / `grant` / `owner_and_grant`；只有 ownership 时 permission 为空，因为应用归属不是授权。
- `roles`：固定 admin/operator/viewer；`{id,name,description,member_count,permissions:{view,investigate,approve,execute,manage},scope}`。成员计数包含未删除的禁用 Mock（页面显示已停用状态），不是可登录人数。operator 的 approve/execute 为 true，仍受应用 operate 与执行审批校验；viewer 不因 operate grant 越过平台上限。
- `departments`：按现有成员 / 团队文本聚合 `{name,member_count,team_count}`；无独立组织树、层级 ACL 或部门 CRUD。
- `applications`：`[{id,name,key}]`；`grants`：`[{application_id,subject_type,subject_id,permission}]`；`mock_notice` 提示来源与限制。

所有集合返回数组而非 null。目录仅管理员可读取，无成员个人资料向查看者泄露。

TeamProfile PATCH 要求同时显式提供 `{owner_id,department}`，省略、null 或部分字段返回 422；空 owner_id 清空负责人；非空 owner 需为该团队未删除成员。新建团队选择负责人时先通过既有接口把该成员加入团队。负责人资料不影响 grants。目录中的有效负责人必须未软删除且仍属于该团队，否则 owner_id / owner_name 均为空。成员和团队 CRUD 继续复用 `/users`、`/teams`、`/teams/{id}/members`；应用权限使用 `/applications/{id}/grants` 的 GET / PUT。PUT 是全量替换，调用方应读取最新内容后修改；既有 API 无 CAS，不能承诺跨管理员编辑绝不冲突。

## PostgreSQL Mock 种子

```sh
python3 scripts/seed-settings-demo.py --runtime .local/product-lab/runtime.json --container aiops-canvas-db-1 --dry-run
python3 scripts/seed-settings-demo.py --runtime .local/product-lab/runtime.json --container aiops-canvas-db-1
```

脚本只允许显式本机 loopback `*_review_*` 数据库，要求 012 已迁移。新增 8 个 `source=settings_demo` 成员、3 个 Mock 团队，仅首次种子事务复用最多 3 个现有应用做示例 view grants；含真实成员的团队不授予。使用 namespaced IDs 和 `.invalid` 邮箱；命名冲突遇到真实记录时事务拒绝。所有示例成员 disabled=true 且 password_hash 为空，数据库 CHECK 阻止启用或设置密码，因而不计入最后启用管理员保护。没有默认可登录账号。

种子不覆盖真实用户、密码、角色、授权，不更改应用负责人或所属团队。重复运行保留已有 Mock 档案；团队成员有人工修改审计后不再补回对应关系；已有首次种子审计后，任何重跑都不新增或补回 grants，即使应用列表变化或 Mock 团队加入真实成员；不重建已软删除成员。Mock 团队授权只涉及 Mock 身份，用户以后明确向团队加入真实成员时才可能使团队授权对该成员生效。

## 验证范围

`go test ./internal/settings` 执行角色语义单测；设置 `TEST_DATABASE_URL` 时另建随机 `settings_test_*` schema，验证迁移、Mock 禁登录、最后真实管理员保护、HTTP 权限、乐观锁、未知字段拒绝、团队负责人、聚合以及事务审计。只清理本测试新建的 schema，不运行其他模块测试，也不调用模型或实验集群。
