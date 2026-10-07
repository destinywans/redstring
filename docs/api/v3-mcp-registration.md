# V3 MCP 首次登记（WAN-238）

`POST /api/v1/mcp-servers` 保存一个应用、当前精确环境下的 MCP 服务器配置，返回 `201`。环境来自已认证会话的 `X-AIOps-Environment`；`all` 禁止写入。该辅助入口补齐新实例登记，不改变原 166 个页面接口的统计口径。

请求示例（所有内容均为占位符）：

```json
{
  "application_id": "orders",
  "title": "Orders MCP",
  "maintainer": "platform",
  "endpoint": "https://mcp.example.invalid/mcp",
  "credential_ref": "orders-token",
  "allow_http": false
}
```

必填 `application_id/title/maintainer/endpoint`；凭据引用可为空。只接受 Streamable HTTP：HTTPS 或部署显式允许的 localhost/127.0.0.1/::1 HTTP。禁止 URL 用户信息、query、fragment、凭据路径穿越；拒绝未知字段（如 token、headers、bindings、reviewed、environment_id）。凭据文件仍由现有 `AIOPS_MCP_CREDENTIAL_ROOT` 管理，登记不会读取或验证凭据。

## 部署准入与授权

必须先由部署配置 `AIOPS_V3_MCP_REGISTRATION_BINDINGS` 对应用、环境、完整端点、凭据引用及 HTTP 选择进行精确准入。配置读取与实例/tier校验见启动配置实现。HTTP 请求不能建立或更改该准入；未配置或不匹配返回 `503 mcp_registration_endpoint_unavailable`。登记本身不访问远程服务。该前置条件用于限制后续发现的地址及凭据，不能替代用户权限。

该环境变量指向绝对路径、0600 普通 JSON 文件，最多 64 KiB、64 项；`instance_id` 必须等于本实例 domain，tier 和 environment 必须精确匹配本实例。配置示例：

```json
{
  "tier": "nonprod",
  "instance_id": "aiops.example.invalid",
  "bindings": [{
    "application_id": "orders",
    "environment_id": "dev",
    "endpoint": "https://mcp.example.invalid/mcp",
    "credential_ref": "orders-token",
    "allow_http": false
  }]
}
```

准入文件在启动时读取；配置变更需要按正常部署流程重新加载进程。它不包含凭据正文，也不证明端点健康。POST 每次创建独立登记，没有重试去重语义；结果不确定时先读列表核对，不能盲目重复提交。

事务内锁定当前活动应用及两份真实环境映射、当前账号、精确会话、角色及授权来源，再核对该应用或其真实 owner team 的 `connectors.manage`。缺失和无权访问的应用均返回相同 `404`，存储或授权读失败明确 `503`。权限/会话/应用变更与登记事务有锁排序；提交前重新核对会话有效期。

同一事务写入登记、版本 1、精确环境绑定及脱敏管理审计；任一步失败全部回滚。版本 JSON 带 `registration_source: "v3"`，以区别历史登记。新记录没有任何旧式 fixed-argument binding。工具审核、开关和发现记录均独立保存，不改写该版本 JSON。

## 后续流程与状态

返回只包含 `id/application_id/environment_id/version/status/bound/reviewed/execution_available/health_verified`；`status=pending`，后三个布尔值均为 false。响应、列表、详情及审计不泄露 endpoint 或 credential_ref。创建成功可立即使用列表及详情读取。

随后由有权用户显式调用 `discover` → `review-candidate` → `review` → 工具开关。零旧式工具绑定不会阻止新登记发现。新 V3 记录每次发现，在网络前及写入观察结果前复核部署准入；准入撤销后仍能读取登记，但不能发出发现请求。取消再绑定也复核目标环境的精确准入。历史登记原有显式只读绑定条件保持。

发现只读取 MCP 声明；远端 read-only hint 不是审批。人工按当前版本审核后，工具仍默认关闭；打开开关也不会提供 Agent 调用权限或执行能力。真实服务可用性、凭据可用性、网络连接、工具实际行为和 Agent 运行治理仍分别验证。

## 验证

`internal/v3mcp/registration_test.go` 覆盖输入和 URL/凭据边界。`TestMCPRegistrationHTTPIsolated` 使用随机 PG schema，测试真实会话与权限、跨应用/环境拒绝、准入缺失/撤销、并发撤权锁、登记读回、零工具登记后的发现/审核/开关流程、审计失败全回滚。MCP 远程发现以函数桩隔离；测试不向外部服务请求，不代表真实 MCP 服务验收。
