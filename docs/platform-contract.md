# WAN-74：应用、环境、权限与连接器契约

本契约对应首版单工作区、多应用 SRE 范围。应用归属与授权分开；管理员必须显式为历史应用分配用户或团队权限。未授权应用不会出现在列表，直接读取、分支、Run、布局、SSE、动作均重新校验所属应用。只读账号不能通过 operate grant 获得写权限。布局是个人状态，具有 view 权限即可保存。

## 数据与管理 API

- `Application`：原有 ID/name/description/environment 保留，新增 key、owner_team_id、maintainer_id、tags、repository_url、runbook_url、archived、environments。
- `Environment`：id、application_id、key、name、archived、binding_revision、bindings。Binding 为 `{kind, connector_id, resource}`；不接受凭据或用户指定目标 URL。
- `GET/POST /api/v1/applications`，`GET/PATCH /api/v1/applications/{id}`；PATCH 为基础字段部分更新。
- `POST /api/v1/applications/{id}/archive` 接受 `{archived:true|false}`，历史资料保留；归档后禁止新调查及操作。
- `GET/POST /api/v1/applications/{id}/environments`；`PATCH /api/v1/environments/{id}` 使用完整可编辑环境字段和当前 binding_revision。过期版本返回 409。环境可归档恢复。
- `GET/POST /api/v1/teams`，`PATCH /api/v1/teams/{id}`；`GET/PUT /api/v1/teams/{id}/members`，PUT `{user_ids:[]}`。
- `GET/PUT /api/v1/applications/{id}/grants`，PUT `{items:[{subject_type:"user"|"team",subject_id,permission:"view"|"operate"}]}`，原子替换。
- `GET /api/v1/audit` 管理员读取最新 250 条管理事件；不包含密码、密钥、原始日志。
- `GET /api/v1/connector-registrations` 管理员读取可绑定连接器及资源标识，不返回服务端凭据或地址。
- 调查创建新增 environment_id；未提供时仅在应用具有唯一未归档环境时允许推断。历史调查迁入 `{appID}-default` 环境。
- Deployment/telemetry GET 接受 `?environment_id=`；metrics/logs POST 体接受 environment_id。

平台仓储接口为 `store.PlatformRepository`；HTTP 共享 `Server.AuthorizeApplication`、`VisibleApplicationIDs` 与 `AuthorizationError`。上下文使用 `store.WithPrincipal` / `Principal`、`WithEnvironment` / `EnvironmentID`。运行任务持久化 actor_id；Worker 每次工具读取前及成功提交前检查最新账号和应用权限。SSE 每次读取事件前检查会话与应用权限。

## 服务端连接器登记

`AIOPS_KUBERNETES_REGISTRY` 指向未入库的 JSON 数组，示例：

```json
[
  {
    "id": "staging-checkout",
    "config": {
      "api_url": "https://cluster.example.invalid",
      "ca_file": "/run/secrets/cluster-ca.crt",
      "token_file": "/run/secrets/cluster-token",
      "namespace": "staging",
      "deployment": "checkout-api"
    },
    "verification": {
      "telemetry_application": "checkout-staging",
      "request_counter_query_id": "business_requests_total",
      "error_counter_query_id": "business_errors_total",
      "sample_age_query_id": "business_source_sample_age",
      "maximum_sample_age_seconds": 30,
      "probe_url": "https://checkout.example.invalid/health"
    }
  }
]
```

`verification` 可省略，表示只读调查，不允许回滚。正常应用回滚必须同时登记业务探测 URL 和受控 Prometheus 查询；请求和错误查询必须返回应用范围内的单序列聚合原始整数累计计数器；第三个查询返回这些真实源样本的最大年龄（秒），不能用 Prometheus 查询求值时间替代源样本时间。请求计数必须排除健康探测、抓取及合成观测请求；源年龄查询需覆盖所有参与聚合的计数器，缺失来源不得返回代表完整观测的零年龄。以上查询由管理员审核登记，运行时不能证明任意 PromQL 的业务含义。指标缺失、NaN、无限值、陈旧数据、计数器不增长、重置或错误累计值变化均不能证明恢复。至少观察 180 秒，每 5 秒检查目标 UID/模板、完整 Deployment 就绪、业务 HTTP 探测和真实流量指标。`Requests` 为当前原始请求累计计数减去本次观察开始时的累计计数，不从速率推算。每个观察间隔要求请求累计值确实增长；抓取周期较长或低流量将保守地不能证明恢复，需调整经审核的观测方案。只有 rate/increase 查询的旧配置不能启用普通应用恢复。

原 `AIOPS_CONNECTOR_CONFIG` 只为 demo 应用保留演示适配器。`/stats`、单副本限制只属于 demo，不传播到新应用。正常应用可不接 Kubernetes，继续使用 Prometheus/Loki 和知识检索。

Prometheus/Loki 继续使用 `AIOPS_TELEMETRY_CONFIG` 中的服务端配置；环境 Binding 的 resource 选择其中 applications 映射键，允许不同环境使用独立指标查询和日志标签。首版每类遥测后端一个登记实例，多 Kubernetes 实例已经支持；扩展多个同类遥测实例需要后续配置形状与适配工作。

历史默认环境只在 binding_revision=1 且无绑定时导入旧连接配置。用户修改或清空之后，不会在重启时自动恢复绑定。

## 回滚与审批身份

新提案摘要包含 environment_id、connector_id、binding_revision、configuration_digest，以及原有 Kubernetes UID、模板与资源状态摘要。批准、入队和 Worker 执行前重新解析并对比绑定；连接器配置或环境变更使旧计划不可执行。旧计划没有明确环境绑定时保持可读，必须重新提案，不能推断目标。执行仍使用原操作 ID 和 Kubernetes CAS / reconciliation，禁止盲目重试写入。

## 迁移和验证边界

`006_platform.sql` 增加团队、成员、应用 grant、环境、审计及运行 actor 字段；默认不授予历史账号任何应用 grant。管理资料和授权更改在同一数据库事务记录审计。

本实现进行受影响 Go 构建及一个纯内存业务指标验证测试。该记录不是数据库迁移、真实模型、真实集群恢复或最终产品验收证明；这些验证必须单独记录对应源码版本、隔离环境和结果。


## 应用资料与环境配置补充（WAN-81）

来源为博客 `src/components/ops/Applications.tsx` 中的应用目录与详情组织方式。沿用当前多环境和权限模型；博客里的模拟健康、部署状态与直接修改集群的含义不作为本产品已实现能力。

应用增加 `profile`：业务归属、服务类型、生命周期、重要程度、开发语言/框架、默认分支、构建命令、镜像仓库和开发文档地址。主负责人/团队使用稳定引用，响应补充可显示名称。命令和地址在此仅登记，不会执行构建、登录仓库或抓取远程 URL。

环境增加 `configuration`：副本数、CPU/内存申请与上限、服务端口、连接池上限、超时、限流、重试及熔断比例。未登记字段为 null，不填演示数值。配置资料按 `configuration_revision` 独立乐观并发控制；修改资料不会下发 Kubernetes，也不会把登记值当真实运行状态。实际资源目标仍由 bindings 管理，原有审批冻结规则继续适用。

应用目录提供关键词、团队/负责人/环境/语言/归档状态筛选与排序，并以分组详情/编辑呈现归属、开发与环境配置。原文登记和管理变更继续记录审计。

迁移 `009_application_profile.sql` 保留旧应用/环境并添加资料默认值；本轮按用户要求未执行迁移、构建或验证。旧版本的构建与运行记录不能作为本修改的证明。
