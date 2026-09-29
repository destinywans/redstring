# V3 我关注的服务：持久关系与状态边界（WAN-120）

`internal/v3serviceviews.Service.RegisterWatched` 提供 `GET /api/v1/me/watched-services`、`PUT /api/v1/me/watched-services/{id}` 和 `DELETE /api/v1/me/watched-services/{id}`。主程序需在已有 `v3serviceviews.New(...)` 实例上调用一次 `RegisterWatched`；共享路由、RBAC 与聚合 OpenAPI 由 WAN-117 接线。本批领域定义追加在 `scripts/v3-openapi-observability.py`。先运行 `140_v3_user_watched_services.sql`，不回填演示关注关系。现有库迁移、真实来源与页面行为尚未验证。

## 关系与授权

- 关注关系仅由当前人类用户明确 PUT 创建，存入 `v3_user_watched_services(user_id,application_id,environment_id)`；本人负责、团队成员、值班及通知订阅均不自动形成关注。PUT 幂等，当前有环境及服务查看权的未归档服务合计最多 50 个；隐藏的旧关注不占这个侧栏容量。另设每人 500 条持久关系的硬上限，避免无限积累；撤销环境授权后留下的历史关系仍占硬上限，达到上限时需要恢复该环境授权后删除旧 ID 或由运维审核清理。实际关系变更与管理审计原子提交。
- `{id}` 是与服务目录/详情一致的 base64url(application ID + NUL + environment ID)，不能使用 Mock 的服务 key。PUT 限单一选定环境，要求有效未归档服务以及当前 `services.view` 应用或现任 owner team 授权。隐藏和不存在的服务同为 404。DELETE 也限当前获准环境，但只删除本人精确 ID 的关系；即使服务归档或应用查看权限撤销，持有先前 ID 的用户仍可取消关注，未存在的关系仍返回 204。环境授权本身撤销后，中间件不接受该环境的 DELETE；该历史关系不占当前可访问环境的关注容量。
- GET 只列当前环境集合内、仍登记且有权查看的本人关系；非生产 `all` 可跨已获准环境读。初次不可见的历史关系留在库中，撤权期间不输出。一次读取最多考察 500 条有效关系、返回 50 个当前可见服务；超限明确 422。最后一次检查锁定当前用户和 Cookie 会话、角色授权表、关系及服务身份，并复核环境/对象授权；读取期间关系或服务资料改变则 409，撤权或注销不输出旧列表。无私人关注时返回真实空列表。

## 状态来源与冻结前端

状态复用 `v3serviceviews.statusOnly`：只在所需服务资料齐备、当前 events 权限、精确 Prometheus dashboard 查询、前后指标绑定封印一致，以及已审核告警覆盖中的真实 firing 告警齐备时返回 `ok | warn | danger`。这表示 SLO burn 与已覆盖告警的服务信号，**不是 Kubernetes readiness**。缺少来源、资料或读状态权限时保留关注项，返回 `status: null`、`availability: {state:"unavailable",reason:"..."}`；不制造健康的 0 值。来源完整时 `availability.state="available"`，本路由不臆造可选 `signal` 文本。任何未知项使顶层 `complete_v3_dto=false`。

冻结 `web/src/v3/api/types.ts` 的 `WatchedService.status` 必填且 `Shell.tsx` 直接把它用于状态点；真实模式需单独适配 null 的视觉和文字状态。Mock 中的裸 key 与固定百分比不是本路由的实证数据。本批不改前端或 Mock。

GET 对授权且来源完整的服务有 200 成功路径；来源不足也返回带显式未知状态的 200，因为个人关注关系本身仍可真实读取。指标绑定和连接器行在最终事务中按现有写路径顺序锁住直至响应；告警覆盖函数需要单独借用数据库连接，因此在最终事务前复核，它的令牌文件与映射检查属于读取时观察，不能声称与响应原子固定。数据库或授权来源错误返回 503，超出关注上限返回 422。当前提交按本阶段要求只做最小静态检查；尚无真实 Prometheus/告警、现有库迁移、前端或浏览器验收证据。
