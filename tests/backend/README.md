# 本地后端正式入口验证

这些用例只连接 `tools/testlab/backend.py` 创建的两个 `cmd/aiops` 实例和独占 PostgreSQL 容器。默认跳过；执行前必须设置 `AIOPS_TESTLAB_BACKEND_ENABLED=dedicated-temp-container` 和 `AIOPS_TESTLAB_BACKEND_FIXTURE`（生成的绝对路径、0600 JSON 文件）。不读取 `.env`，不连接业务数据库。每次连接前检查 loopback 地址、专用数据库名和容器 `aiops.testlab.owner` 标签。

从项目根目录运行 `python3 tools/testlab/backend.py entry` 可准备正式入口并执行全部 `-race` 用例。没有该隔离环境时运行 `go test ./tests/backend` 只证明编译与显式跳过。

- 迁移：真实 SQL 的完整文件名账本、两个并发调用、重复启动、151/152 前的合成旧数据升级、整批失败回滚、真实 `pg_dump`/`pg_restore`。备份覆盖本次创建的随机 schema，不等于完整产品数据库灾备验收；旧数据夹具也不是生产历史数据库副本。
- 接口：真实登录、401/403、session 撤销、跨环境读取、双实例 ETag 防覆盖、API Token 一次展示/消费/撤销、Canvas 创建读取、Runbook 草稿创建读取、对话接受后异步完成、人工事件幂等及状态变化、绑定结束事件的复盘和不同人发布。
- 降级：未连接身份证据源必须明确 503，不能提供伪造的已验证身份状态。

RBAC 测试用户与传入告警记录是明确的合成数据；模型调用使用本地协议 Mock，不能证明真实 IdP、Alertmanager、飞书、Notion、Confluence 或付费模型连接与权限。专业协议的完整边界用例仍由对应模块测试覆盖。没有执行的接口不能由本目录的成功结果推定覆盖。

每次执行向 `.local/testlab/interface-results.jsonl` 追加记录，使用本轮 ID 和唯一调用 ID；入口运行器只读取启动时文件偏移之后的新调用，重复运行不会截断首次证据。记录模板路径、预期/实际状态、场景及路由证据；不记录请求正文、Cookie、Bearer token 或响应正文。401/403 不作为已注册业务路由的证据。

用例生成唯一资源 ID，只清理本轮子资源、恢复工作区设置、撤销生成 Token、注销/禁用合成人员并归档合成服务。审计以及 Token 撤销历史保留在独占临时数据库中，不能清空共享 app DB。失败记录保存于 `.local/testlab/first-failures`，修复后的验证记录保存于 `.local/testlab/fix-regressions`。
