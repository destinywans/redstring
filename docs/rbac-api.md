# 角色、权限与基础设施资源操作（WAN-105）

## 当前授权模型

角色关联权限、用户和部门。成员至多属于一个部门，有效资源权限为直接角色及该部门角色的并集。

权限定义只有唯一名称和描述，本身没有操作或资源类型。授权存放于中间表 `rbac_permission_resources(permission_id,scope_type,resource_id,action)`；`scope_type` 固定为 `resource`。同一权限关联一个资源时可以选择一个或多个操作。空权限没有关联记录，也不授予操作能力。内部稳定 ID 用于外键与 API，不在列表展示编码。

资源树只使用 `cmdb_resources` 中的基础设施资源，与基础设施页面使用同一资源目录。当前类型白名单为 `cluster,node,deployment,namespace,host,network,storage,redis,mysql,kafka`。不再加入应用资料、应用访问与审批、平台管理对象等虚拟分组。Pod/容器作为工作负载运行实例，Kubernetes Event 作为运行记录保留；两者不单独进入 CMDB 授权树，通过明确的父资源关系继承查看范围。

| 关联操作 | 授权对象 | 当前实际行为 |
| --- | --- | --- |
| 查看 view | 明确关联的基础设施资源 | 查看资源目录；应用可见性由关联资源的查看授权推导 |
| 变更 update | 明确关联的基础设施资源 | 修改目录资料；恢复执行还要求精确目标的变更授权和已有人工批准 |
| 审批 approve | 明确关联的基础设施资源 | 恢复提案批准、拒绝与撤销时检查精确资源目标 |
| 删除 delete | 明确关联的基础设施资源 | 删除允许手动管理的目录记录，不直接删除集群资源 |

四种操作在每个资源叶子均可配置。配置授权不意味着该资源已经具备所有操作的执行器；业务端点仍检查资源类型、来源和执行前提。调查、执行、管理不再作为资源关联操作，旧 scope 或旧操作不能通过新请求保存。

## 平台管理与恢复操作

平台管理资格来自实际存在的 `admin` 角色，通过用户直接关联或所在部门关联获得。启用、未删除的真实账号才可成为有效管理员；单独保留 `users.role` 字段或创建同名角色不会绕过实际角色关联检查。管理员身份用于设置、成员、部门、角色权限等平台管理，不替代资源操作授权。

至少保留一个启用、未删除、非示例且有登录凭据的实际管理员。角色、用户、部门关联变动或账户禁用/删除等导致最后管理员消失时返回 `409 last_rbac_manager`，事务回滚。

当前恢复提案针对 Deployment：必须唯一匹配一条非 Mock 基础设施资源，并精确核对应用、环境、连接器 source_id、命名空间、名称和 UID。审批检查该资源的 `approve`，执行检查同一目标的 `update`；人工批准摘要校验和独立执行器仍保留。目标缺失、匹配歧义或只有 Mock 资源时拒绝真实恢复操作。Mock 资源可用于界面和权限配置，不代表真实执行能力。

## 权限与角色 API

主入口 `/api/v1/rbac`。管理接口要求实际平台管理员身份。`GET /me` 返回 `{management_objects:[...]}` 供现有设置导航使用；它是导航兼容结构，不表示权限表中存在管理对象。`GET /catalog` 返回四项 `actions:[{id,name}]` 和 `departments:[{id,name}]`，没有应用或管理对象资源候选。

`GET/POST /roles|permissions`、`GET/PATCH/DELETE /roles|permissions/{id}`。列表返回 `{items,total,page,page_size}`。名称清理首尾空格，数据库唯一约束处理并发重名，分别返回 `409 role_name_conflict` 或 `409 permission_name_conflict`。名称最多 200 字符，描述最多 2000 字符。更新和删除必须提交 revision，版本冲突返回 409。

创建权限只提交基本信息；修改时追加 revision：

```json
{"name":"基础设施日常运维","description":"用于明确关联资源的日常操作"}
```

Permission 返回 `{id,name,description,builtin,revision,resource_count,actions,created_at,updated_at}`；同一资源多个操作只计为一个关联资源。`actions` 汇总真实关联资源上的查看/变更/审批/删除，空权限返回空数组；这不是权限定义的可写字段。定义仍不接受 action、scope_type 等授权字段。

权限列表可传 `action` 和 `resource_kind`，组合条件必须命中同一条资源操作关联；服务端总数与分页共用过滤。`q` 支持中文、英文、全拼及首字母，具体规则见 `docs/search.md`。创建/更新时间以 ISO 时间返回；历史未记录的时间为 null，前端显示“—”，新建及后续资料/资源关联变动记录时间。

初始化角色与权限同样允许编辑和删除，builtin 只记录初始化来源。删除在事务内解除关联，不删除另一侧的用户、部门、角色、权限或资源；删除权限同时递增引用角色的 revision。界面使用居中确认弹窗并说明授权影响。最后管理员保护仍有效。

## 资源选择树

`GET /permissions/{id}/resource-tree?q=&kind=&limit=50&offset=0`。`GET /permissions/{id}/resources` 暂作同形状别名，不返回旧分页 Candidate 表格。

```json
{
  "revision":3,
  "groups":[{
    "kind":"resource:host","label":"主机","total":75,"offset":0,"has_more":true,
    "items":[{
      "id":"host-1","name":"主机一","scope_type":"resource","kind":"host",
      "description":"","is_mock":false,"missing":false,
      "assigned_actions":["view","update"],
      "available_actions":["view","update","approve","delete"]
    }]
  }]
}
```

分组键为 `resource:<基础设施资源类型>`；叶子 scope_type 始终为 resource。无 kind 时返回各组前 limit 条；指定 kind 和 offset 可在组内加载更多。limit 为 1–100，offset 为非负数。检索匹配资源名称或 ID。只返回当前目录中的基础设施资源，不返回历史虚拟 scope 或失效目标分组。

界面按资源类型折叠展示，输入 200ms 后检索，回车立即检索，匹配结果展开。没有列表页码或“跨页保留选择”。顶部选择一个或多个操作后，勾选资源添加这些操作；取消勾选只移除这些操作，保留该资源的其他操作。部分命中为半选。没有选择操作时禁止编辑叶子。

## 保存资源操作

`PUT /permissions/{id}/resources`：

```json
{
  "revision":3,
  "upsert":[
    {"resource_id":"host-1","scope_type":"resource","actions":["view","update"]},
    {"resource_id":"deployment-1","scope_type":"resource","actions":["view","approve"]}
  ],
  "remove":[{"resource_id":"host-2","scope_type":"resource"}]
}
```

upsert 替换明确指定资源的完整操作集；前端合并该叶子其他已保存操作后发送。未编辑、未加载或当前搜索未展示的资源不属于本次修改集合，不能用当前可见树替换全部关联。remove 移除指定资源的全部关联操作。

每条 upsert 至少一个操作，最多四项且不重复。未知资源、非基础设施资源、重复目标、同一目标同时 upsert/remove、其他 scope 或其他操作均被拒绝，任一错误整笔回滚。保存时重查平台管理资格和 revision。

## 角色关联与账户保护

`GET /roles/{id}/associations/{permissions|users|departments}` 保留分页检索。`PUT /roles/{id}/associations` 提交 `{revision,permissions:{add,remove},users:{add,remove},departments:{add,remove}}`，三个页签同事务保存。角色关联命名权限，实际资源授权由权限中的资源操作记录决定。

仅 `origin=legacy` 的账户基础角色关联返回 `locked=true` 与 lock_reason，由成员管理调整；直接移除返回 `409 legacy_role_membership`。普通用户与部门关联可管理，角色整体不因此变成只读。账户、部门和角色变动仍保留管理员资格重查与最后管理员保护。

## 迁移与默认数据

- `014_rbac.sql` 建立角色、权限、关联和单部门模型。
- `015_rbac_permission_scope.sql`、`016_rbac_resource_actions.sql` 保留为既有设计的历史迁移；016 把操作迁入关联表，删除权限定义的操作和范围字段。
- `017_rbac_editable_defaults.sql` 允许初始化记录编辑删除，账户不会在重启时复活已删除基础角色。创建账号或明确改为已删除基础角色返回 `409 base_role_unavailable`。
- `018_rbac_infrastructure_permissions.sql` 确立基础设施类型白名单、四操作和实际管理员角色身份。旧虚拟 scope 与旧操作被有效权限视图排除；数据库约束阻止新写入。迁移不删除已有用户库的历史数据。无账号且仅存在已知初始化种子的全新库可一次性初始化为四角色、四权限；已有评审库的重置由父任务在用户授权下显式执行。

默认权限为资源查看、资源变更、资源审批、资源删除。默认角色为平台管理员、运维人员、只读成员和变更审批员；具体初始化资源关联只覆盖初始化时已有基础设施资源，新资源不会无条件自动获授权限。运行库迁移及显式重置是否执行由交付记录说明，不把源码更新当作数据库执行证据。CI/CD 继续暂停。

- `019_search_pinyin.sql` 建立共享拼音匹配字典与函数。
- `020_rbac_permission_timestamps.sql` 为新权限和后续更新记录时间，既有历史时间保持未知；关联变化通过触发器更新。

## 验证边界

本轮仅做受影响的定向检查，不运行全平台测试。类型检查、构建、隔离 PostgreSQL 检查、运行库迁移和浏览器验证分别记录在交付证据中；本契约不把计划中的检查描述为已通过。
