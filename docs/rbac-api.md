# 角色、权限与资源操作（WAN-105）

## 当前授权模型

角色关联权限、用户和部门。成员至多属于一个部门，有效权限为直接角色及该部门角色的并集。

权限是只有唯一名称和描述的集合，本身没有操作或资源类型。实际授权来自中间表 `rbac_permission_resources(permission_id,scope_type,resource_id,action)`；同一资源可以关联多个操作，未关联任何资源的权限不会产生任何能力。内部稳定 ID 用于外键与 API，列表不展示编码。

| 关联操作 | 可新建范围 | 实际检查 |
| --- | --- | --- |
| 查看 view | resource、application | 资源目录查看、应用上下文读取 |
| 变更 update | resource | 修改资源目录资料 |
| 审批 approve | application | 动作提案的实际批准、拒绝、撤销检查 |
| 删除 delete | resource | 删除允许手动管理的目录资源，不直接删除集群资源 |

应用在资源树中单独分组，审批绑定应用 ID，并由现有 `AuthorizeApplication(app,"approve")` 读取。普通资源节点不提供不可执行的审批选项。历史 investigate、execute、manage 及 legacy_application 动作保留为旧关联数据，不重命名、不合并为新动作，也不进入新授权操作选项。保留审批、执行分离以及明确批准和独立执行器约束。

自定义角色不会修改 `users.role`。删除初始化的基础角色后，旧模块不再继承该角色的隐式身份能力；显式 RBAC 授权仍按实际关联判断。CI/CD 继续暂停。

## 迁移与兼容边界

- `014_rbac.sql` 建立角色、权限、关联与单部门模型。历史多部门收敛前，已有应用 ACL 折算保留；基础角色的 `origin=legacy` 用户关联继续由账户触发器维护。
- `015_rbac_permission_scope.sql` 是此前的权限级应用范围设计，保留为历史迁移，不重写已有迁移。
- `016_rbac_resource_actions.sql` 将旧权限操作、范围迁入每条资源关联，删除权限表的 action、scope_type、resource_scope_mode 三列。未编辑的 legacy_acl 先冻结为迁移时已有应用 ID；显式范围保持原目标。既有当前应用访问继续与原成员应用 ACL 相交，不扩大权限；迁移后新应用不再自动进入旧兼容权限。
- 有效权限视图改为资源关联 INNER JOIN：没有关联的命名权限没有操作。历史管理/调查/执行操作仍保留在原资源目标上。
- schema_migrations 保证每个迁移只执行一次，重启不覆盖之后的关联调整。运行库迁移是否执行由发布环节单独记录。

## 权限与角色 API

主入口 `/api/v1/rbac`。管理接口要求 `manage:management:rbac`；`GET /me` 只读取本人管理对象。`GET /catalog` 的 actions 为四个 `{id,name}`，不含 scope_type。

`GET/POST /roles|permissions`、`GET/PATCH/DELETE /roles|permissions/{id}`。列表维持 `{items,total,page,page_size}`；名称首尾空格清理，数据库唯一约束处理并发重名，分别返回 `409 role_name_conflict` 或 `409 permission_name_conflict`。名称最多 200 字符，描述最多 2000 字符。更新/删除必须提交 revision，版本冲突返回 409。

新建/修改权限只接受基本信息：

```json
{"name":"应用运维","description":"用于指定应用与资源的日常操作","revision":2}
```

Permission 返回 `{id,name,description,builtin,revision,resource_count}`，resource_count 为不同 `(scope_type,resource_id)` 数量，多动作不会重复计数。旧 action/scope_type 请求字段被 JSON 校验拒绝。目录仍可兼容 action/scope_type 查询参数，其匹配来自实际资源关联，界面不再显示权限级操作筛选。

初始化角色与权限同样允许编辑和删除，builtin 只记录初始化来源，不再限制操作。删除在事务内解除中间表关联，不删除关联另一侧的用户、部门、角色、权限或资源；删除权限同时递增引用角色的 revision。删除前重查实际管理权限，删除后检查至少保留一个有效管理者，失败整笔回滚。界面使用居中确认弹窗，明确关联授权将解除。

初始化记录只在版本迁移中播种，不会在重启时复活。`017_rbac_editable_defaults.sql` 调整账户基础角色同步：只关联仍存在的基础角色，创建或显式改为已删除基础角色返回 `409 base_role_unavailable`；已有账户资料仍可更新。具体运行迁移状态见本轮交付记录。

## 资源选择树

`GET /permissions/{id}/resource-tree?q=&kind=&limit=50&offset=0`。旧 `GET /permissions/{id}/resources` 暂作同形状别名，不再返回旧分页 Candidate 结构。

```json
{
  "revision":3,
  "groups":[{
    "kind":"resource:host","label":"主机","total":75,"offset":0,"has_more":true,
    "items":[{
      "id":"host-1","name":"主机一","scope_type":"resource","kind":"host",
      "description":"","is_mock":false,"missing":false,
      "assigned_actions":["view","update"],"available_actions":["view","update","delete"]
    }]
  }]
}
```

kind 分组键为 `resource:<实际资源类型>`、`application`、`legacy_application`、`management`。叶子 kind 保留实际类型供图标显示，唯一标识由 scope_type 与 id 组成。无 kind 时返回各组前 limit 条，指定 kind + offset 可在该组加载更多；limit 1–100，offset 非负。检索匹配资源名称或 ID。

界面默认折叠到资源类型，检索时展开匹配结果，不提供页码或跨页保留选择。应用资料与应用访问/审批分别标明，避免不同授权目标出现同名分组。

资源目录及应用是可新增目标；历史应用/管理对象仅显示此权限已有关联。失效目标仍作为 missing 叶子显示，允许移除，不能新增操作。assigned_actions 是服务端保存的完整动作；available_actions 是允许新选的操作。未展示、未加载资源不属于本次修改集合，不能拿当前树替换全部关联。

## 按资源保存动作

`PUT /permissions/{id}/resources`：

```json
{
  "revision":3,
  "upsert":[
    {"resource_id":"host-1","scope_type":"resource","actions":["view","update"]},
    {"resource_id":"app-1","scope_type":"application","actions":["view","approve"]}
  ],
  "remove":[{"resource_id":"host-2","scope_type":"resource"}]
}
```

upsert 替换明确指定资源的完整动作集；其他资源完全保留。前端增删当前勾选动作时需合并保存该叶子的其他动作。每条 upsert 必须至少一个动作，不允许重复动作、重复目标、同一目标同时 upsert/remove、未知新目标或不兼容的新操作；任一失败整笔回滚。历史操作仅允许同权限、同 scope、同资源已经存在者原样保留，不能通过请求凭空新增历史能力。remove 移除该资源全部动作，也允许清理失效目标。

## 角色关联与保护

`GET /roles/{id}/associations/{permissions|users|departments}` 保留原分页及草稿过滤。`PUT /roles/{id}/associations` 提交 `{revision,permissions:{add,remove},users:{add,remove},departments:{add,remove}}`，三个页签同事务保存。角色可以关联所有命名权限；实际授权始终来自权限内已保存资源动作。

仅用户 `origin=legacy` 基础角色关联返回 `locked=true` 与 lock_reason，由成员管理调整，直接删除返回 `409 legacy_role_membership`。普通用户和部门关联可管理，整个角色不会因此变成只读。

关联、资源授权保存与账户/部门成员写入共享 advisory lock；拿锁后重查操作者管理能力。提交前保留至少一个启用、未删除、非示例且有登录凭据的有效 RBAC 管理者，否则 `409 last_rbac_manager`，包括成员禁用/删除/角色变更和部门成员移出迁移。资源删除与关联编辑共享资源锁，删除时仅移除 scope_type=resource 的匹配关联并增加权限版本，不误删同 ID 的应用/管理对象关联。

viewer 基础角色可配置，不能按字符串假定只读。创建或修改具有额外资源授权的 viewer 身份、密码、邮箱、启用状态需要额外 manage:rbac；禁用账户也按潜在关联权限保护。`manage:users` 不能单独复制高权限身份。默认仅原兼容只读权限的 viewer 保持原成员管理行为。

## 验证

`internal/rbac` 的定向 PostgreSQL 测试在随机 schema 中运行迁移、实际权限检查、资源多动作、空权限、审批与执行隔离、未知/重复/冲突输入、未加载关联保留、历史动作边界、树检索与加载更多，以及最后管理者和成员身份保护；结束仅删除该测试 schema。没有集群操作。运行库迁移、服务更新和浏览器验收由父任务另行验证。
