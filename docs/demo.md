# 专用真实故障演示环境

仅使用新建的 `aiops-canvas-demo` kind 集群。所有 kubectl 命令显式指定 `.local/aiops-canvas-demo/kubeconfig` 和 `kind-aiops-canvas-demo` context；不修改默认 kubeconfig，不接触 `cka-daily` 或 `portfolio-agent-lab`。Kubernetes API 仅绑定 `127.0.0.1`，业务 Service 没有宿主端口或公开入口。

## 创建与取证

需要 Docker、kind、kubectl、Go、Python 3、rg。固定 kind node 镜像 `kindest/node:v1.35.8`；应用编译为无外部依赖的 Go 二进制并放入 scratch 镜像。两个 Pod 各限制 100m CPU/64Mi 内存，kind 控制平面另占资源。

```sh
bash scripts/lab-up.sh
go run ./demo/labcli evidence
bash scripts/lab-inject.sh
go run ./demo/labcli evidence
```

沙箱如限制默认 Go 缓存，命令前设 `GOCACHE="$PWD/.local/go-cache"`。脚本为 buildx 使用专用本地状态目录。重复 `lab-up` 会把演示 Deployment 重置为 stable，并刷新凭据；调查过程中不要执行它。

`checkout` 提供 `/work` 业务请求、`/health` 业务健康、`/ready` 容器就绪、`/metrics` Prometheus 文本和 `/stats` 结构化计数。独立 `traffic` Pod 每秒通过 Service 发起真实 HTTP 请求。修改 Deployment 的 `MODE=regression` 产生 HTTP 500 和日志，容器仍 Ready，展示“发布成功但业务失败”。

`connector.json` 记录 API 地址、CA 和令牌文件路径，不内嵌令牌。对应 ServiceAccount 仅在 aiops-demo 内：读 checkout Deployment、读 ReplicaSets/Pods/日志、GET 指定 Service proxy、PATCH checkout Deployment；不能读取 Secrets、执行 exec、修改其他 Deployment 或删除资源。令牌请求有效期 24 小时（实际以 API Server 签发为准）；到期重新运行 setup。`.local` 不提交版本库。

## 不可变提案、执行与恢复

产品必须由身份权限与持久化审批模块批准原始计划摘要后调用适配器。`demo/labcli execute` 仅是开发者显式调用的适配器测试入口，**不构成产品审批链验收**。

```sh
go run ./demo/labcli -revision 1 propose > .local/aiops-canvas-demo/plan.json
# 明确审阅 plan.json 后的专用演示测试：
go run ./demo/labcli -plan .local/aiops-canvas-demo/plan.json -operation lab-test-001 execute
go run ./demo/labcli -plan .local/aiops-canvas-demo/plan.json -operation lab-test-001 reconcile
kubectl --kubeconfig .local/aiops-canvas-demo/kubeconfig --context kind-aiops-canvas-demo -n aiops-demo rollout status deployment/checkout
bash scripts/lab-observe.sh
```

不要把 revision 1 当作永远正确的恢复目标；正式 API 应从取证得到的已登记 Deployment 历史版本选择并冻结模板。ReplicaSet 的 pod-template-hash 被移除，其余模板保留。提案绑定 namespace/name、UID、完整原 spec 摘要、目标模板、目标版本、180 秒观察条件，摘要按 JSON 语义规范化，允许 PG jsonb 调整键顺序。

执行先读实际对象，检查 UID 与 spec；JSON Patch 再检查 UID、resourceVersion 和当前模板，并在**同一个请求**记录 operation annotation 与替换模板。状态更新引起的资源版本变化若发生在读取后，CAS 也会失败，首版保守交由重新审查，不自动重发。网络失败、HTTP 5xx 或无法解析响应视为未知结果；只读 reconcile 同时核对 UID、operation 和目标模板。未发现 marker 不能证明没有延迟请求，返回 needs_review，不自动重试。

产品验证调用 `ObservePlan`，每个样本检查批准目标 UID/模板；`Observe` 是未绑定提案的开发环境健康观察。两者均要求至少 180 秒、每 5 秒检查：Deployment 期望代次已观测、新副本全部可用且无旧副本；业务健康通过；同一计数器实例每个区间有新增请求且没有新增失败。实例改变、计数器重置、空流量或任何失败都会使该次观察失败；由上层明确重启观察，不悄悄缩短窗口。

当前指标适配器针对**单副本演示服务**，多副本 Service 轮询不能作为聚合指标使用；生产应接 Prometheus 等聚合、带时间窗口的观测源。证据读取每个 Pod 最多 30 行/16KiB，最多 10 个 Pod。执行器仅回滚已登记 Deployment Pod 模板，不支持任意 kubectl、Shell、ConfigMap/数据库迁移。

## 接入后端

后端配置 `AIOPS_CONNECTOR_CONFIG` 指向上述 JSON，由应用注册配置关联 `demo`/Checkout API。`connectors.New` 构建实例，`ReadEvidence` 暴露只读材料；`ProposeRollback` 只构造提案；审批与执行 Worker 才持有 `Execute` 路径。程序通过 HTTPS 验证自定义 CA，拒绝重定向；不调用 Shell。

独立服务账户目前合并了受限读与 PATCH 权限。应用的只读取证工具不暴露写方法；未来可进一步分别配置读、写身份。PG 审批状态、幂等执行记录和 Worker 租约由应用层负责，该外部适配器不声称提供端到端 exactly-once。

测试：`go test -race ./internal/connectors ./demo/...`。覆盖 spec 漂移、计划篡改、CAS 结构与拒绝、504 后不重试、只读结果核对、jsonb 顺序变化、空流量/计数器变化和 ReplicaSet 模板清理。

## 本次验证记录（2026-09-19）

专用真实集群已创建，限制权限的 ServiceAccount 成功读证据和执行条件回滚。stable 样本 19 次请求/0 错误；regression 样本 25 次请求/25 错误；回滚返回 applied、generation 3。原始证据保存在 `.local/aiops-canvas-demo/evidence-stable.json`、`evidence-regression.json`、`plan.json`。连续三分钟观察已通过：`recovered=true`，36 个采样区间、179 次新增真实业务请求；开始 `2026-09-19T01:59:40.931841Z`，结束 `2026-09-19T02:02:40.992835Z`，原始结果见 `recovery.json`。此为外部适配器与演示环境验收，尚不代表产品登录、模型调查、PG 审批与执行审计端到端全部通过。

删除专用环境：`bash scripts/lab-down.sh`；不会删除其他集群。保留本地证据与凭据文件，按需自行清理 `.local/aiops-canvas-demo`。

设计依据：[Kubernetes 条件更新语义](https://kubernetes.io/docs/reference/using-api/api-concepts/#updates-to-existing-resources)、[Deployment 状态](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#deployment-status)。

## Prometheus 与 Loki 扩展：代码已写，等待允许测试

**本节新增实现尚未部署或做功能验收。** 用户要求写完后不要测试，必须等用户明确允许。新增协议用例、集群清单和验证程序只作为源码交付；不能把上面的 Kubernetes/demo 已验证记录当作本节 Prometheus/Loki 验证结果。

`internal/connectors/telemetry.go` 通过 Prometheus `/api/v1/query_range` 和 Loki `/loki/api/v1/query_range` 读取时间窗数据。后端设置 `AIOPS_TELEMETRY_CONFIG`，调用 `LoadTelemetryConfig`、`NewTelemetry`，随后使用：

```go
QueryMetrics(ctx, applicationID, queryID, window) // 已登记 PromQL 查询的 ID
QueryLogs(ctx, applicationID, window, limit)     // 固定的应用标签选择器
```

请求接口没有原始 PromQL、LogQL、URL、凭据或 selector 参数。PromQL 由管理员在对应 application 配置中完整登记；管理员负责审核其中每个表达式确实限定了应用数据，适配器不会对任意 PromQL 做字符串拼接或猜测标签语义。模型只能选择该应用的查询 ID。Loki 的标签由配置生成精确等值选择器，标签值经过字符串转义；返回的 stream 标签还会再次核对应用范围。应用授权由 HTTP/工具调用上层负责，不能把“出现在配置中”当作用户获得了访问权限。

默认时间窗是最近 15 分钟，指标步长 15 秒，日志最多 100 行；服务器配置更严格时，未提供的默认窗口/日志数会缩到配置上限，默认步长会同时满足最小步长和每条序列采样预算。明确提供但超范围的请求仍失败，不悄悄改写。默认单次查询最多一小时、回看七天、至少五秒步长，每条序列最多 1000 点、最多 100 条序列，日志配置上限 1000 行。启动时拒绝最小步长大于最大窗口、每条序列预算少于两点等不自洽配置。响应保留来源地址、provider、application、已登记查询、时间窗、HTTP 状态、采集时间、warnings、结构化失败与数据。空结果显式 `empty=true`，不解释为“健康”；达到条数上限标记 `limit_reached=true`，提示可能不完整。原生 histogram 需使用登记的 scalar reduction 查询，当前不静默丢弃 histogram 数据。

端点默认要求 HTTPS，支持额外 CA、Bearer token 文件或 Basic password 文件，可设置固定 Loki tenant ID。凭据不进入证据，拒绝 HTTP 重定向。仅明确配置 `allow_http: true` 才允许 HTTP。请求默认 15 秒超时、响应最大 4 MiB；调用者 context 取消/超时也会终止请求。地址与文件路径是服务器配置，模型无权修改。

配置结构示例（不是实际凭据）：

```json
{
  "prometheus": {
    "url": "https://metrics.example.internal",
    "bearer_token_file": "/run/secrets/prometheus-token"
  },
  "loki": {
    "url": "https://logs.example.internal",
    "bearer_token_file": "/run/secrets/loki-token",
    "tenant_id": "operations"
  },
  "applications": {
    "demo": {
      "prometheus_queries": {
        "requests": "sum(rate(checkout_requests_total{application=\"demo\"}[1m]))",
        "errors": "sum(rate(checkout_errors_total{application=\"demo\"}[1m]))"
      },
      "loki_labels": {"application": "demo", "namespace": "aiops-demo"}
    }
  }
}
```

### 待授权的独立遥测演示

`demo/telemetry/resources.yaml` 为现有 **aiops-canvas-demo** 专用集群准备三个有资源限制的组件：Prometheus、单进程 Loki、微型日志转发器。没有公开端口，没有完整 Grafana/运维套件。Prometheus 直接抓取现有 checkout `/metrics`；转发器通过单独的只读 ServiceAccount 读取真实、带时间戳的 checkout Pod 日志，并调用 Loki push API。模型使用的遥测读取账户只具有两个 Service proxy 的 GET 权限，和日志转发写入 Loki 的路径分离。

待用户明确允许后，执行顺序为：

```sh
# 会构建镜像和部署专用遥测资源；当前不要执行。
bash scripts/lab-telemetry-up.sh
# 至少积累约 1 分钟 scrape 后，再运行真实协议验收。
bash scripts/lab-telemetry-verify.sh > .local/aiops-canvas-demo/telemetry-evidence.json
```

脚本不调用 `lab-up.sh`，不会把现有 checkout 改回 stable 或注入故障。生成的 `telemetry.json` 使用 Kubernetes HTTPS service proxy 和独立短期令牌，只在回环 API 上访问遥测服务。清单固定 `prom/prometheus:v3.13.3`、`grafana/loki:3.7.0`，依据官方发布/安装资料选择，**尚未拉取或验证这些镜像与清单的运行兼容性**。

验收程序要求真正查询到非空指标与日志，保存完整来源和时间窗；预备的 httptest 源码覆盖登记范围、选择器转义、超时、上游错误、凭据与重定向、空结果和实际响应格式，目前未运行。测试放行后还需确认指标值反映持续真实流量、日志来自 checkout、错误/空数据不被误报健康，不能仅凭进程 Ready 判断接入完成。

本地遥测数据使用 emptyDir，重建 Pod 会丢失数据；微型转发器没有持久化游标、完整重试队列或生产采集保障，生产应使用现有 Prometheus/Loki 与标准采集器。删除专用遥测资源的脚本为 `scripts/lab-telemetry-down.sh`，不会删除 checkout、traffic 或集群；当前同样无需执行。

协议与镜像参考：[Prometheus HTTP API](https://prometheus.io/docs/prometheus/latest/querying/api/)、[Prometheus 发布版本](https://prometheus.io/download/)、[Loki HTTP API](https://grafana.com/docs/loki/latest/reference/loki-http-api/)、[Loki Docker 安装](https://grafana.com/docs/loki/latest/setup/install/docker/)。
