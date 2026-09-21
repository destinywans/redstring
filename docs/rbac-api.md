# 角色、权限与唯一部门（WAN-105）

## 授权模型

`rbac_roles`、`rbac_permissions` 及三类关联表是实际 PostgreSQL 授权来源。有效权限为用户直接角色与唯一部门继承角色的并集。每条权限的动作与 scope_type 创建后固定，关联的目标 ID 可通过有版本的增删修改。

| 动作 | 范围 | 实际检查位置 |
| --- | --- | --- |
| view | resource | 资源目录单个资源、列表与关系图 |
| update / delete | resource | 修改目录资料 / 删除人工登记目录资源；不会操作集群 |
| investigate | application | 调查与分支操作；调查 worker 开始、工具执行与结果提交重新检查 |
| approve | application | 动作批准、拒绝、撤销；动作执行前重新检查批准者 |
| execute | application | 已批准动作入队；执行前重新检查请求者 |
| manage | management | rbac、users、teams、settings、applications、resources、audit 各自独立对象 |

调查、审批、执行可读取所在应用上下文，但三种写能力互不提升。`manage:resources` 只管理资源登记入口，不代替某条资源的 update/delete。`manage:users` 不能单独变更平台角色、创建高权限身份或替换高权限身份凭据；这些操作额外需要 `manage:rbac`。`manage:teams` 更改已关联角色的部门成员（包括加入、移出及从已有角色部门转移）同样额外需要 `manage:rbac`，并与部门角色授予串行化检查。固定管理员的授权通过真实权限关联保留，最后启用管理员保护继续生效。

其他尚未接入细粒度 RBAC 的旧模块仍执行原有身份边界，不因自定义角色提升用户的 `users.role`。暂停的 delivery/deployment 不属于本轮新增管理对象。

动作的精确批准摘要、冻结绑定、目标状态和执行器校验继续保留。动作服务 `Register` 绑定独立动作授权钩子；connector resolver 仅做应用可读与绑定校验，不再把 approve/execute 合并为 investigate。

## 一次性迁移与兼容

`014_rbac.sql` 在现有 Store.Migrate 事务执行，无运行时重复 Seed。

- 沿用产品现有 `teams` 作为部门，`team_members(user_id)` 唯一约束确保每人至多一个部门。`users.department_id` 随成员关系触发器同步；旧 `users.department` 文字仅用于首次迁移择优，不另建平行部门体系。
- 历史多部门：优先保留名字与 `users.department` 完全匹配的现有部门，其余按稳定 ID 选首个。删除其他部门关系前，将它们已有应用 ACL 折算为该用户直接授权；viewer 的 operate 上限保持 view。迁移审计记录保留/移除部门。没有新角色的虚构继承。
- admin/operator/viewer 作为受保护兼容角色，operator/viewer 仍依赖既有应用 ACL，不自动获得新应用权限。账户创建、角色变更和软删除通过触发器同步固定 legacy 关联，不覆盖自定义关联。示例身份 `settings_demo` 不获分配、不进入有效权限视图。
- 三个可编辑业务角色“资源只读”“应用诊断”“变更审批”，关联迁移时实际目录/未归档应用的显式快照，均不分配给任何用户或部门。Mock 目录对象可以关联，仍带 `is_mock`；这不代表有真实生产基础设施授权。
- 内置资源权限绑定迁移时存在的对象，不随未来资源自动扩大。管理对象枚举仅列已接入的对象。

## API 与分页编辑

主入口 `/api/v1/rbac`。管理接口需要 manage/management/rbac；`GET /me` 仅返回当前用户的 `management_objects`，供入口显示。`GET /catalog` 返回固定动作、管理对象及应用/部门筛选元数据。

新建权限仅提供查看（view/resource）、变更（update/resource）、审批（approve/application）、删除（delete/resource）四项，POST 同样拒绝其他操作。历史 investigate、execute、manage 权限及其执行边界继续保留，不因新建入口精简而删除或重解释；内部维护和既有记录仍使用原动作。

角色与权限分别支持 `GET/POST /roles|permissions`、`GET/PATCH/DELETE /roles|permissions/{id}`。列表支持 `q,page,page_size`（默认 1/10，最大 100）、权限 action/scope_type、builtin。返回 `{items,total,page,page_size}`。更新/删除带 revision；冲突 409。内置记录的基本资料及删除仍受保护；内置角色的权限、用户和部门关联可管理。角色有任何权限/用户/部门关联时不可删除；被角色引用的权限不可删除。

角色名称清理首尾空格后由已有数据库唯一约束检查，创建及改名重复时返回 `409 role_name_conflict`。名称最多 200 个字符，描述最多 2000 个字符（按 Unicode 字符计数）。内部稳定 ID 继续用于外键、关联和 API，不要求用户输入，也不作为角色列表的副标题展示。

权限名称同样由现有唯一约束校验，创建及改名重复返回 `409 permission_name_conflict`。权限内部 ID 继续用于关联，列表不展示编码。所有权限（包括内置权限）都可管理关联目标，内置权限的基本资料和删除边界保持原约束。

候选接口：

- `GET /roles/{id}/associations/{permissions|users|departments}`
- `GET /permissions/{id}/resources`

候选分页还返回 `revision`，支持 selected=all|yes|no，action/scope_type/kind/application_id/department_id。`add_ids/remove_ids` 支持逗号或重复 query 参数，用于服务端按未保存草稿筛选“已选”；响应 `selected` 始终是已保存的直接关联。继承源在 `inherited/source_departments` 单独显示。软删除用户的已有关联仍可见并可移除。

用户候选增加 `locked` 与可选 `lock_reason`。仅 `rbac_user_roles.origin='legacy'` 的基础角色关联锁定，必须从成员管理变更，直接删除返回 `409 legacy_role_membership`；普通直接关联和部门关联仍可增删。此限制不会把整个内置角色变成只读，也不会自动降级账户或改写旧模块身份。兼容应用权限仅供内置角色关联，可移除和重新添加；自定义角色继续使用明确资源/应用范围的权限。

`PUT /roles/{id}/associations` 一次提交：

```json
{"revision":2,"permissions":{"add":["p"],"remove":[]},"users":{"add":["u"],"remove":[]},"departments":{"add":[],"remove":["d"]}}
```

三个标签页在同一事务、同一角色版本中保存并审计。`PUT /permissions/{id}/resources` 使用 `{revision,add,remove}`。新增目标必须存在；移除按现存关联校验，允许清理已失效目标。重复/冲突增删、未知新增 ID 会拒绝整笔事务。界面不能用当前页替换全部关系。

`015_rbac_permission_scope.sql` 增加 `resource_scope_mode`（`explicit | legacy_acl`）：普通权限默认 explicit，旧兼容应用权限初始化为 legacy_acl，不修改任何旧应用 ACL。尚未编辑的兼容权限继续按原 ACL 工作，其当前应用候选全部选中，resource_count 为当前应用数量。第一次保存关联时在同一事务把当前应用候选冻结为显式 ID，应用 add/remove 后切换 explicit；之后实际访问始终为显式应用与用户原 ACL 的交集。清空显式关联表示无应用，不能退回无限制；新增应用也不自动扩入已冻结范围。权限详情、目录计数与角色关联中的计数使用同一规则。迁移由既有 schema_migrations 控制仅执行一次，重启不会重置已管理范围。

关联及权限资源保存事务与账户/部门成员写入使用相同 advisory lock，获得锁后重新检查操作者的 `manage:management:rbac`。提交前检查仍有启用、未删除、非示例且具备登录凭据的有效 RBAC 管理者；撤销最后管理者返回 `409 last_rbac_manager`，整笔关联变更及版本更新回滚。成员禁用/删除/基础角色变更、部门成员移除和迁移复用同一有效管理者检查，避免把管理权移交自定义角色后绕过原固定管理员保护。

基础角色现在可变更权限，`viewer` 字符串本身不再意味着低权限。仅保留原 `view:legacy_application` 兼容只读权限（或无权限）的 viewer 可由成员管理员直接创建；若 viewer 被授予其他显式资源或管理等权限，创建此身份额外要求 `manage:rbac`。修改此类账户的密码、邮箱、启用状态或删除账户同样检查真实及潜在角色授权，禁用账户也不会绕过身份保护。

资源删除与权限资源关联变更共享事务 advisory lock。资源删除同时移除精确 resource scope 关联并增加受影响权限版本，避免并发重新挂入失效资源。

`GET/POST /departments` 与 `PATCH /departments/{id}` 操作同一 teams 部门；`PATCH /users/{id}/department {department_id:null|string}` 明确迁移/清除该用户唯一部门，不偷偷变更其他部门成员。个人 `/profile` 返回至多一条 departments、实际有效角色 `roles[{id,name,source,department_id?}]` 与动作摘要。

## 验证边界

`internal/rbac/postgres_test.go::TestRBACPostgresBoundaries` 在随机 schema 执行历史迁移与实际 PostgreSQL 授权/分页/原子更新检查，结束删除该 schema；不会迁移运行库或发出集群操作。测试包括用户管理越权、失效用户关联清理、资源删除与失效资源关联清理。运行库迁移、浏览器产品验收和 Git 交付由父任务独立记录。

`role_refinement_test.go` 定向覆盖 Unicode 字符限制、重复名称、内置角色关联增删、基础关联锁定、管理权限交接后的最后管理者保护，以及扩权 viewer 的身份创建/凭据修改边界；同样只在新建随机 schema 中写入并清理。

`permission_refinement_test.go` 覆盖四项新建契约、权限重名、内置资源及管理目标编辑、legacy 候选与计数、显式应用与旧 ACL 交集、清空/重加/未来应用隔离、版本冲突和迁移重复执行不覆盖配置。运行库是否执行 015 迁移必须由发布环节单独验证。
