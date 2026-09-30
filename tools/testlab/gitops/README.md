# 专用 GitOps 验证

`gitops.py prepare|gitlab|kind|verify` 只管理当前 testlab owner 的全新 GitLab、kind、Argo 和 Prometheus。组件探针证明协议、TLS、精确规则组与独立审查 API 可用，不能证明产品规则启用。`gitops.approval_api` 保留 `product_chain_verified_by_this_probe=false` 和剩余验收范围；完整产品链单独由 `gitops.py chain` 的真实结果判定。缺少真实审批协议仍返回 blocked 和非零退出码。

`chain` 复用当前源码指纹匹配的正式非 race 二进制与专用 APP DB，通过第三个临时 HTTPS 入口执行正式调查创建、提案、两个独立账户的软件 ES256 WebAuthn 批准、worker 创建的准确 MR、独立 GitLab 审查与批准合并、Argo 同步、Prometheus 精确规则读回。软件认证器不代表硬件 Passkey 验收；不插入批准记录，不改写正式 API 返回的调查 ID，不启用自动合并。

第三入口为 `https://testlab.local:18583`，内部固定 loopback 18585。客户端仅在进程内把该 TLS 主机名连接到 127.0.0.1，不改系统 hosts、DNS 或默认 Kubernetes context。入口独占 Popen，正常退出和失败都停止该进程。两个既有 HTTP 实例、runtime config 与 soak hooks 保持原值。

故障 HTTP 证据只保存 path/status/error，无请求、Cookie 或令牌；按 invocation UUID、序号、状态和路径摘要以 O_EXCL 写入独立 0600 文件，`chain-last-red.json` 仅为最新文件指针。执行观察使用显式 READ ONLY 事务；自有合成用户的 RBAC seed 是独立写事务。正式完整链未成功时保留真实首红，不替换为组件 MR 或模拟成功。

所有权由精确容器名及 `aiops.testlab.owner` label 校验。唯一公开清理入口为 `testlab.py cleanup --run-id`；本工具没有 cleanup 子命令。中央清理器负责 active run、第三入口和全部候选 owner 校验；不运行 kind delete，不删除共享网络，不切换默认 context。

冻结前完成组件初始化、完整链、正式基线与故障验证，再进入负载窗口。`prepare/kind/verify` 会更新专用配置并可能重启 Argo，因此不能在正式性能或 soak 冻结窗口运行。重型组件资源背景和缺失采样需在报告中保留，不能把环境资源约束算作产品性能结论。
