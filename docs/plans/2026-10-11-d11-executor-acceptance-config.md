# D11 告警规则执行器：验收实例配置（2026-10-11）

本文给出两个验收实例（nonprod 环境 `test`、prod 环境 `prod`）启用告警规则执行器所需的全部配置、标签规范、告警路由建议和部署步骤。文中只有 Vault 引用和 ID，没有任何令牌值。

## 1. 前提

**对应提交**：

| 提交 | 内容 |
| --- | --- |
| `b51903f8`、`2a374535` | 执行器合并、`file_pull` 加载、读回与对账，迁移 175 |
| `35cac23f` | 启用不再先导出草稿 MR，迁移 176 |
| `4e360d52` | GitLab 私有 CA（激活、变更读取、知识源） |
| `818fa458` | `POST /executions/{id}/recheck` |
| `0af4ac00` | D32 标签规范与告警摄取的环境隔离 |
| `dee3cd07` | 停用动作，迁移 177 |
| 本文之后的提交 | 提案、执行、规则列表的结构化字段（§11.4–11.6），迁移 178 |

**值的来源**：
- 实验侧：`aiops-test/docs/plans/2026-10-11-d11-lab-prep.md`；
- 验收库的只读查询：checkout-api 的应用 ID、环境行 ID、Prometheus 连接器端点；
- 实验 Prometheus `/api/v1/rules` 和 GitLab `GET /projects/6` 的只读读取：组名、`file` 路径、GitLab 外部地址都已核对。

**已有、不用改**：
- `AIOPS_VAULT_URL`、`AIOPS_VAULT_TOKEN`：执行器用进程的 Vault 令牌读写令牌。实验侧的 `aiops-product-read` 策略已包含 `aiops/lab/gitlab/prometheus-rules`。
- `AIOPS_V3_PASSKEY_ENABLED=true`：两人批准需要它。

写令牌不是连接器凭据，**不要**加进 `AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS`。

## 2. 环境变量

| 变量 | 作用 | 文件要求 |
| --- | --- | --- |
| `AIOPS_V3_ALERT_RULES_CONFIG` | 规则来源：用哪个 Prometheus、哪些规则组属于哪个应用、规则的 `application` 标签值。规则列表和启用都依赖它 | 绝对路径，≤ 64 KiB |
| `AIOPS_V3_ALERT_RULE_ENABLE_CONFIG` | 写入批准：可写的 GitLab 项目、分支、文件、加载方式和写令牌引用。**设了它 worker 就会启动**，没有单独的开关；只对绑定里出现的环境就绪 | 绝对路径，普通文件，权限 0600，≤ 128 KiB |
| `AIOPS_OPERATIONS_CONFIG` | 告警来源映射。prod 已有；nonprod 要接 test 规则的告警时才需要（第 6 节） | 绝对路径；令牌文件 0600 |

启动时不读 Vault，也不访问 GitLab 或 Prometheus。配置写错时进程会启动失败，并给出原因。

## 3. nonprod（环境 `test`）

checkout-api 的应用 ID 是 `0a5cb01131f3c2387b848004e8f5db23`，`test` 环境行 ID 是 `1f0649144effff46430748571f03cff2`。

`AIOPS_V3_ALERT_RULES_CONFIG`（建议 `.local/acceptance/nonprod/alert-rules.json`）：

```json
{
  "environments": {
    "test": {"url": "http://127.0.0.1:19090", "allow_http": true}
  },
  "bindings": [
    {"environment_id": "test", "application_id": "0a5cb01131f3c2387b848004e8f5db23",
     "file": "/etc/redstring-rules/current/rules/test/checkout-api.yml", "group": "redstring-test-checkout-api",
     "application_label": "checkout-api"}
  ]
}
```

`AIOPS_V3_ALERT_RULE_ENABLE_CONFIG`（建议 `.local/acceptance/nonprod/alert-rule-enable.json`，0600）：

```json
{
  "gitlab_url": "https://127.0.0.1:18446",
  "gitlab_token_ref": "vault://aiops/lab/gitlab/prometheus-rules#token",
  "gitlab_ca_field": "ca",
  "gitlab_web_url": "http://127.0.0.1:28080",
  "bindings": [
    {"environment_id": "test", "application_id": "0a5cb01131f3c2387b848004e8f5db23",
     "group": "redstring-test-checkout-api",
     "provider_file": "/etc/redstring-rules/current/rules/test/checkout-api.yml",
     "project_id": 6, "target_branch": "main", "yaml_path": "rules/test/checkout-api.yml",
     "load": "file_pull", "readback_timeout_seconds": 300,
     "project_path": "aiops-lab/prometheus-rules", "connector_id": "9cf0ccfeff69213747e3ded07ba998af"}
  ]
}
```

## 4. prod（环境 `prod`）

checkout-api 的应用 ID 是 `f79b3ba7bc6224bbdcf7e9114099991a`。

`AIOPS_V3_ALERT_RULES_CONFIG`（建议 `.local/acceptance/prod/alert-rules.json`）：

```json
{
  "environments": {
    "prod": {"url": "http://127.0.0.1:19090", "allow_http": true}
  },
  "bindings": [
    {"environment_id": "prod", "application_id": "f79b3ba7bc6224bbdcf7e9114099991a",
     "file": "/etc/redstring-rules/current/rules/prod/checkout-api.yml", "group": "redstring-prod-checkout-api",
     "application_label": "checkout-api"}
  ]
}
```

`AIOPS_V3_ALERT_RULE_ENABLE_CONFIG`（建议 `.local/acceptance/prod/alert-rule-enable.json`，0600）：

```json
{
  "gitlab_url": "https://127.0.0.1:18446",
  "gitlab_token_ref": "vault://aiops/lab/gitlab/prometheus-rules#token",
  "gitlab_ca_field": "ca",
  "gitlab_web_url": "http://127.0.0.1:28080",
  "bindings": [
    {"environment_id": "prod", "application_id": "f79b3ba7bc6224bbdcf7e9114099991a",
     "group": "redstring-prod-checkout-api",
     "provider_file": "/etc/redstring-rules/current/rules/prod/checkout-api.yml",
     "project_id": 6, "target_branch": "main", "yaml_path": "rules/prod/checkout-api.yml",
     "load": "file_pull", "readback_timeout_seconds": 300,
     "project_path": "aiops-lab/prometheus-rules"}
  ]
}
```

prod 现有的 `AIOPS_OPERATIONS_CONFIG`（`.local/acceptance/prod/operations.json`）不用改。它的 checkout-api 映射 `{application: checkout-api, redstring_env: prod}` 就是产品规则告警会命中的映射。

## 5. 字段说明与标签规范（D32）

**字段**：
- `file`（规则来源）和 `provider_file`（写入批准）：都是 Prometheus 在 `/api/v1/rules` 里报告的 `file`，必须逐字一致；两份配置里同一组的值也要相同。git-sync 的 `current` 链接让这个路径在换提交后不变。
- `yaml_path`：规则仓库里的文件路径；`group` 是文件里的组名。仓库是项目 6（`aiops-lab/prometheus-rules`），`main` 受保护，合并方式为 fast-forward。
- `load: "file_pull"`：两人批准后由执行器自己合并（fast-forward，带 `sha`），Prometheus 经 git-sync 加载。这种绑定不能再写任何 `argo_*`、`repo_url`；配置里没有 `argo_cd` 绑定时，`argo_url`、`argo_token_file` 也不能写。
- `readback_timeout_seconds`：取值 60–3600，默认 300。
  - 实测从合并到 `/api/v1/rules` 可见约 23–30 秒，再过一次求值（10 秒）`health` 变为 `ok`。
  - 超时记 `load_timeout`，worker 在合并后 24 小时内继续读回；超过 24 小时用执行页的“重新核对”。
  - 这个值计入批准内容，改了它，已批准但未执行的提案会因计划变化而失效。
- `gitlab_token_ref`、`gitlab_ca_field`：写令牌和它旁边的 `ca` 字段（验收 TLS 代理的证书），每次请求都从 Vault 读，不缓存。读不到时不发请求。
- `project_path`、`connector_id`：只用于展示（提案页、执行页的仓库链接与连接器），不进批准范围，可以不写。nonprod 的 `connector_id` 是该实例的 GitLab 连接器；prod 现在没有 GitLab 连接器，不写。
- `gitlab_web_url`：GitLab 自报的外部地址，`GET /projects/6` 返回的 `web_url` 以 `http://127.0.0.1:28080` 开头。它只用于核对 MR 链接，产品不会访问它。

**标签规范**：产品写入的每条规则都带下面两个作用域标签，告警摄取靠它们对到服务、并留在自己的环境里。

| 标签 | 值 | 说明 |
| --- | --- | --- |
| `application` | 应用 key，如 `checkout-api` | 取自规则来源绑定的 `application_label`，和告警来源映射用的值相同。不配置时为应用 ID（旧约定） |
| `environment` | 环境 ID：`test`、`prod` | prod 实例的告警摄取会拒收与映射环境不一致的告警（第 6 节） |

- 请求没带这两个标签时由服务端补齐；带了但不一致返回 422。
- 启用和停用提案的审查内容都有一行 `Scope labels: environment=… application=…`。
- 规则列表、写入校验、同名规则检查、YAML 导入（按标签反查应用）和执行器都按 `application_label` 判断。实验的占位规则 `RedstringRuleGroupPlaceholder` 标的是 `application: checkout-api`，所以会出现在产品规则列表里。
- 同一环境里，一个应用只能有一个标签值，一个标签值也只能对应一个应用；违反时启动失败。

## 6. 告警摄取与 Alertmanager 路由

**prod 实例的行为**：
- 告警先按映射选中应用和环境。
- 如果告警自带 `environment` 标签、且与映射环境的 key 不同，就不入库，记一条日志（来源、alertname、两个环境），计入响应的 `ignored`。
- 同一批里的其他告警照常入库，Alertmanager 不会重发这一批。
- 不带 `environment` 标签的现有实验告警不受影响。

**前提（D33）：实验告警必须经过这条重标记。** 实验 VM 集群的告警从应用和节点指标继承了 `environment="lab"`（如 LabHigh5xxRate、LabAPIDown）。按上面的规则，prod 实例会拒收它们。实验 Prometheus 的 `alert_relabel_configs` 因此要有这两条（2026-10-11 已在实验侧生效）：

```yaml
alert_relabel_configs:
  - source_labels: [cluster]
    regex: aiops-vm-cluster
    target_label: redstring_env
    replacement: prod
  - source_labels: [cluster, environment]
    regex: aiops-vm-cluster;lab
    target_label: environment
    replacement: prod
```

第二条只改写 `environment="lab"`，产品规则带的 `environment="test"`、`environment="prod"` 原样保留。所以 test 规则的告警仍不会被 prod 收下，prod 规则和实验告警都能对上服务。测试用的是 LabHigh5xxRate 的真实标签集：未经重标记时被拒收，重标记后入库，并对到 checkout-api。

**建议的路由**（实验侧修改）：
- 实验的重标记会给所有 `cluster="aiops-vm-cluster"` 的告警加 `redstring_env="prod"`，包括 test 组规则的告警。
- 所以在现有送 prod 实例的路由上加一个匹配器：

  ```yaml
  - receiver: <现有 prod 实例 webhook>
    matchers:
      - redstring_env="prod"
      - environment=~"prod|"    # 不存在的标签按空串匹配，所以也收下不带 environment 的现有实验告警
  ```

- `environment=~"dev|test|staging"` 的告警不能落到 prod 接收器，默认接收器也不能是它。这类告警要么送 nonprod，要么丢弃。
- prod 侧的拒收是第二道防线，不替代路由。

**nonprod 的告警来源**：
- nonprod 现在没有配置 `AIOPS_OPERATIONS_CONFIG`，test 规则的告警目前没有地方摄取。
- 要接的话，新建 `.local/acceptance/nonprod/operations.json`：

  ```json
  {
    "sources": [
      {"id": "lab-alertmanager-nonprod",
       "token_file": "/Users/wans/Home/code/codex/aiops/.local/acceptance/nonprod/ops-lab-alertmanager.token",
       "mappings": [
         {"labels": {"application": "checkout-api", "environment": "test"},
          "application_id": "0a5cb01131f3c2387b848004e8f5db23", "environment_id": "1f0649144effff46430748571f03cff2"}
       ]}
    ],
    "feishu_channels": []
  }
  ```

- 令牌文件：0600，至少 24 个字符，由实验侧生成；同一个值作为 Alertmanager 接收器的 Bearer 令牌。
- 接收器地址：`/api/v1/webhooks/alertmanager/lab-alertmanager-nonprod`，主机写法与现有 prod 接收器相同，端口换成 nonprod 的 18091（`AIOPS_ADDR=127.0.0.1:18091`）。
- 对应的路由匹配器：`environment="test"`，另加 `application` 匹配，或者不加。

## 7. 部署步骤

1. **构建**：用含上述提交的集成分支构建，替换验收二进制。迁移 175–178 在启动时自动执行。它们只改 `v3_alert_rule_enable_executions` 和 `v3_proposal_actions` 的约束和列，不改写已有行。
2. **写配置**：
   - 按第 3、4 节写两个实例的两份 JSON。写入批准文件设为 0600。
   - 在各自的环境文件里设 `AIOPS_V3_ALERT_RULES_CONFIG` 和 `AIOPS_V3_ALERT_RULE_ENABLE_CONFIG`。
   - nonprod 如果要接告警，再设 `AIOPS_OPERATIONS_CONFIG`（第 6 节）。
3. **重启两个实例并核对**：
   - 日志里没有 `configure V3 alert rule enable` 或 `alert rule configuration` 错误。
   - 带 `X-AIOps-Environment: test`（或 `prod`）请求 `GET /api/v1/alert-rules`，列表里有 `RedstringRuleGroupPlaceholder`。
   - `POST /api/v1/alert-rules`（`mode: enabled`）不再返回 503 `enable_not_connected`。
4. **实验侧**：按第 6 节修改 Alertmanager 路由；nonprod 要接告警时再加接收器。
5. **走查**（每个实例各一遍）：
   1. 在 checkout-api 的开放调查里，用 `mode: enabled` 提一条规则，例如 `vector(1) > 0` 加上 `severity: P3`，标签由服务端补齐。这条规则加载后会立即触发，便于核对告警链路；走查结束时停用它。
   2. 两名非发起人用通行密钥批准。
   3. 执行页依次出现 `reserved → committing → merging → merged → loading → enabled`，从合并到生效约 30–40 秒。
   4. GitLab 项目 6 的 `main` 是执行器的 fast-forward 提交，MR 标题为 `Review AIOps alert rule …`。
   5. 规则列表里出现这条新规则。prod 实例收到它的告警后，事件对到 checkout-api。
   6. 对这条规则调用 `POST /api/v1/alert-rules/{id}/disable`（`{ "investigation_id": … }` 加 `Idempotency-Key`），两人批准后执行页走到 `disabled`，规则从列表和 Prometheus 中消失。
6. **回滚**：
   - 去掉 `AIOPS_V3_ALERT_RULE_ENABLE_CONFIG` 并重启：worker 停止，新的启用和停用请求明确失败。
   - 已合并并已加载的规则不会因此消失，需要走停用（重新配置后）或在仓库里另行处理。
   - 不要删除未核对的执行分支、MR 或执行记录。

## 8. 注意

- D32 改了启用提案（意图版本 2）的审查内容。如果在 D32 之前的构建上建过启用提案，升级后它会校验失败，需要重新提出。执行器此前没有在实例上配置过，正常情况下不存在这种提案。
- 配置同步（跨实例提升规则）仍要求 `application` 标签等于应用 ID；验收实例没有配置它。
- 试运行（`dry_run`）的隔离查询校验的是指标序列的标签，与规则标签无关。
- 只有 `file_pull` 绑定能停用，停用不需要新增配置。
