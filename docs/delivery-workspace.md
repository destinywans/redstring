# 交付中心与 AI 扩展接线

对应 WAN-89～94。用户批准的范围与取舍在 [实施计划](plans/2026-09-20-delivery-workspace.md) 和 [记忆／AI 扩展决策](decisions/0010-memory-and-ai-extensions.md)。本文件记录代码接口与运行边界；当轮证据另见 releases。

## 模块与配置

Go 服务按需装配 `internal/delivery`、`internal/deployment`、`internal/aiextensions` 和 `internal/memoryview`。模块 Migrate 在 Store.Migrate 之后执行，不修改 ADK 的存储结构。前端对应 delivery、aiextensions、memory 三个懒加载功能模块。所有接口通过既有 Cookie、角色和应用 ACL；前端按钮隐藏不替代后端授权。

- `AIOPS_DELIVERY_CONFIG`：GitLab、Argo Workflows、S3 及允许的产物地址。字段定义见 `internal/delivery/service.go`。
- `AIOPS_DEPLOYMENT_CONFIG`：已登记 GitOps／Compose／systemd 目标、固定健康条件与私有凭据引用。字段见 `internal/deployment/types.go`。不接受浏览器提交任意 SSH 主机、命令或 kubeconfig。
- `AIOPS_MCP_CREDENTIAL_ROOT`：0700 目录，凭据文件 0600；产品接口只提交文件引用。完整 MCP/Skills 契约见 [模块说明](../internal/aiextensions/README.md)。
- `scripts/connect-delivery-lab.py --lab /path/to/lab`：只向回环地址的隔离 review 工作区登记 Delivery Demo 与私有配置，不触发执行。实验目录需先完成 delivery 的准备流程（生成 `.local/delivery`）；这套实验环境是作者自己的，不随本仓库发布。
- `scripts/verify-delivery-product.py`：默认仅检查配置；`--execute` 才会在明确的专用实验目标创建运行、批准并发布。真实运行耗时包含每目标连续 180 秒健康观察，不能拿构建完成代替发布验收。

凭据、运行数据库、实验令牌、验证输出和生成制品位于忽略的 `.local`，不入 Git。核心工作区不启用原生流水线时仍可单独通过 Compose 运行。

## 定义、运行与恢复

GitLab 流程保留外部权威。导入只关联真实流水线；触发、手动 Job、取消与重试由配置中的项目、分支和 Job 名白名单限制。操作先记意图，再发出请求；请求不确定时先核对外部身份。Webhook 只提示重新读取，不直接信任通知里的状态。

原生定义不可变版本化。React Flow 的业务节点编译为 Argo DAG，允许容器任务、审批、发布、健康验证。源码获取、测试与构建是具体容器任务模板；普通构建不持有发布凭据。`when` 只比较直接上游的输出（eq/ne）；`join:selected` 要求所有被选择分支成功，未选分支明确跳过，并至少有一个成功分支。循环、动态节点、运行中改图及任意回退不在本版。

三个模板均为源码获取 → 并行单元检查／静态检查 → 镜像或 tar.gz 构建 → 审批 → 发布 → 健康验证。审批摘要绑定应用环境、源码 SHA、产物摘要与目标配置；删除审批节点不能使服务端发布授权失效。发布节点和健康节点分别记录状态。

GitOps 只管理登记路径里的直接 JSON Deployment；不执行用户 Helm 插件或任意渲染程序。批准后 CAS 推进 Git 引用并同步固定 SHA，拒绝 Argo CD 自动同步或配置漂移。受控环境禁止旧直接 Deployment 回滚。VM 使用固定 SSH helper，只操作登记 Compose project／systemd unit；同一环境串行，业务进程使用独立无 sudo 用户。

外部结果不明时保留环境锁并转待处理；后续先核对现状，不自动再发写命令。取消不是恢复。当前健康依据是业务 HTTP 和实际产物／发布状态持续稳定，尚不包含 Prometheus 错误率或流量窗口条件。

## 归档与保留

源文件、检查报告和日志保存在 S3 兼容存储；镜像进入 OCI registry，systemd 使用单文件 tar.gz 与摘要。Pod 日志不存在时按记录的归档地址读取；归档失效与无日志明确报错，不伪造成功。

PG 保存至少 30 天保留期、人工 hold 和发布引用。当前发布、审批及恢复引用受保护；本版仅登记满足条件的回收申请，由管理员处理，**没有自动删除存储对象的后台清理器**。这避免清理唯一证据或当前恢复制品，自动清理需要后续验证。API 的 `cleanup_mode=manual_only` 明确此边界。

## 记忆与工具边界

记忆页展示已有权威数据：长期 Runbook／经验、索引状态、最近实际检索引用、分支上下文修订与消息的模型来源。它不是额外的模型隐式记忆库。长期记录编辑生成新草稿版本，归档停止后续召回；保留历史证据，没有物理删除入口。上下文快照只读，继续、派生或合并通过调查页创建新版本。权限复用知识维护／应用授权。

Skills 与 Prompt 显式选择、预览后带入草稿，发送后正文保存版本出处。组合本身不调用模型，也不授予工具或执行权限。MCP 首版为管理员登记的 Streamable HTTP 客户端，固定参数、只读工具白名单；不启动 stdio 进程、不提供 OAuth 流程或对外 MCP Server。
