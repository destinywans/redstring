# V3 连接器凭据绑定

V3 进程从 Vault 读取连接器凭据前，必须在服务端私有配置中精确批准用途。`connectors.manage` 允许登记和管理连接器，不允许自行授权进程级 Vault 令牌读取任意引用并发送到任意端点。

## 配置与启用

1. 通过 `POST /api/v1/connectors` 登记连接器，记录返回的 `id`。需要凭据的登记默认禁用；Vault 引用可以在登记或激活时提交，但请求本身不是使用授权。
2. 部署方独立核对实例层级、环境、连接器 ID、供应商、端点和 Vault 引用。在私有运行配置中设置 `AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS` 为 JSON 数组，重启 V3 进程加载。不要把真实引用或 Vault token 提交进仓库。
3. 对禁用的非 GitLab 登记调用 `POST /api/v1/connectors/{id}/activate`，请求体为 `{ "credential_ref": "vault://mount/path#field" }`；若登记时已保存相同引用，可传 `{}`。无认证来源也可传 `{}`。GitLab 仍须先绑定明确的数字项目 ID，再用 Vault 引用激活。激活成功后，按需用 PATCH 开启已登记的只读来源工具偏好。
4. 用连接测试和详情读取检查结果。通用激活只证明一次有界只读探测获得 2xx；有些健康端点不会验证凭据，因此响应的 `credential_verified: false` 不代表令牌权限已验收。GitLab 激活验证的是指定项目的一次 GET，仍不证明令牌只有只读权限。`webhook_active` 始终为 `false`。

配置示例仅含占位符：

```sh
AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS='[{"tier":"nonprod","environment_id":"dev","connector_id":"generated-connector-id","provider":"prometheus","endpoint":"https://metrics.example.invalid","credential_ref":"vault://mount/path#field"}]'
```

绑定按六项精确匹配；端点只统一末尾斜杠。配置为空时拒绝所有 V3 连接器 Vault 读取。无效、重复或超过 200 项的配置使 V3 启动失败，避免部分规则生效。Vault 地址与进程令牌仍分别由 `AIOPS_VAULT_URL`、`AIOPS_VAULT_TOKEN` 提供；部署方须限制该令牌的 Vault 权限。

此绑定只约束凭据读取，不是连接器的网络出口白名单。具有 `connectors.manage` 的账号仍可登记无认证 HTTPS 来源并触发有界只读探测，既有登记的读取也会访问配置的来源。当前代码尚未对解析后的目标地址实施统一出口准入；部署前应将连接器管理权限限定给可信基础设施管理员，并由网络出口策略限制进程可达目标。若要向更广泛的管理员开放此权限，需另行实现覆盖探测与所有读取器的服务端目标准入，并验证 DNS 变化、代理和跳转边界。

## 既有登记与恢复

未列入绑定的既有凭据连接器不会向 Vault 或上游发送凭据；列表和详情将其呈现为未启用、未验证，工具设置的 `connector_enabled` 为 `false`。数据库原有的 `enabled` 值不在启动时批量改写。新增或修正精确绑定并重启后，原先已启用的登记可重新读取；应立即运行 `POST /connectors/{id}/test` 刷新一次性连接观察。新登记须先激活。

若绑定错误或需停止凭据使用，从私有配置移除该项并重启；缺绑定即停止解析，数据和审计保留。恢复时重新独立核对六项、加回绑定、重启并做连接测试。不要以恢复旧的无限制解析器作为常规回滚方式。没有真实 Vault 和上游的环境只验证本地协议响应；`credential_not_authorized` 表示服务端未批准用途，`credential_unavailable` 表示已批准但 Vault 凭据不可用。
