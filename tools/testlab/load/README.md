# 独立后端性能工具

`python3 tools/testlab/perf.py self-check` 仅做 12 秒真实 HTTP/k6/SSE 接线检查，绝不计作性能或 24 小时验收。`prepare --profile base|scale` 写入自有 APP DB 的真实 schema，生成独立密码和 20 个受权用户，提交后读回数量。仅新增/更新 `perf-{owner}-` 行，不清除他人数据；已经存在规模档时，基础档验收会拒绝。

`load --profile base|scale` 必须在正式二进制、源码和配置冻结后执行：120 秒预热；10/25/50 RPS 各 300 秒；100 RPS 60 秒。基础 25 RPS 执行 DB P95≤500ms/P99≤2s、聚合 P95≤2s/P99≤5s、非预期错误率<0.1%、零 dropped iterations。其他阶段报告饱和点，不冒充同一延迟门槛。SSE 使用 50 正常、10 小接收缓冲区真实慢读 socket；消息生产者是明确标注的合成 DB 数据，正式 HTTP 重连使用 Last-Event-ID。

`soak --duration 86400` 执行同一冻结窗口的连续真实 10 RPS、实际试运行/Webhook、模型协议对话及终止 cursor 重连、真实续登、2 正常/1 慢读 SSE。短时长会拒绝；进程重启、源码/运行配置/令牌内容改变、监控心跳中断 >120 秒、生成器错误或 iteration 缺失均不能通过。仅凭 Python 墙钟经过 24 小时不足以通过，还核对 k6 实测 duration 和 iterations。不拼接中断时间。

运行需要私有 `<owner work>/perf/soak-hooks.json`（0600）：

```json
{
  "dryrun": {"account": "admin", "method": "POST", "path": "/api/v1/实际端点", "body": {}, "expected_status": 202},
  "webhook": {"method": "POST", "path": "/api/v1/webhooks/alertmanager/实际source", "body": {}, "headers": {}, "expected_status": 200},
  "interval_seconds": 60,
  "trial_status_path": "/api/v1/实际trial状态端点",
  "trial_expected_statuses": ["以正式API定义为准"]
}
```

未提供真实 hooks/trial 状态契约时标记 blocked，不替换模拟。body 可用 `${TESTLAB_NOW_RFC3339}` / `${TESTLAB_EVENT_ID}` 生成当前时间和唯一事件；headers 在私有文件中配置，绝不输出令牌。admin hook 通过正式登录取会话。

k6 固定 [官方 1.5.0 release](https://github.com/grafana/k6/releases/tag/v1.5.0) 及镜像 digest `sha256:2072ea9eafa596532d9aee0cc0e0a50cfb0e7fb703981a46179af5f4f22c5ea4`。实际测量路径是对话列表/详情、inbox、alert-groups、home；服务 seed 为真实注册元数据，未伪造服务指标/发布来源。模型为 protocol_mock，耗时不证明付费模型推理性能。日志/摘要/资源采样在自有私有目录，CPU/RSS/FD/堆或 goroutine 读不到时显式列入 missing，不填零。

`python3 -m unittest discover -s tools/testlab/load -p 'test_*.py'` 验证冻结边界，属于离线逻辑证据。

真实负载边界（2026-09-30）：

- 低比例写入为 4% `POST /alerts/{id}/claim`：20 个本轮 seed 告警各自由对应合成用户认领一次，此后走同一用户的幂等事务重试；不持续新增对象/审计，不修改其他数据。写延迟/错误独立计量，并计入 DB 延迟门槛。
- 会话续登使用实际 `sessions.expires_at`，并在 k6 后续登录读取 Cookie `Max-Age`，提前 5 分钟续登。当前正式 API/DB 实测 12 小时，不能假设 24 小时或固定 1 小时。
- scale 的 500000 条消息分配到 1000 个会话（每会话 500 条）；升级旧 seed 时仅按本轮消息 ID 重映射父会话，不删除已有对象。明细正常路径不超过产品每会话 1000 条限制。
- 全局 inbox / alert-groups / home 有真实完整聚合安全上限，不能用 app ACL 或过滤参数绕过预先候选扫描。scale 正常负载只验当前可支持的会话 list/detail 与单告警 detail/claim；聚合接口的实际拒绝语义单列，不宣称该规模全接口通过。base 与 scale 均先实际 HTTP preflight；只有明确 503/pagination_required 或 503/home_result_limit 的限额拒绝可单列 boundary 通过、对应聚合性能 blocked，其余非预期错误仍失败。受支持 list/detail/单告警/claim 路径继续测量，但完整聚合 P95/P99 未验收。不删除其他对象、缩小同环境 seed 或更换环境掩盖限额。
- 终端事件顶层 metrics 提供真实 k6 吞吐、P50/P95/P99、错误、写指标、资源样本、missing、trial 实际窗口与重叠。running 顶层 elapsed/planned_seconds/current_test 可直接供看板使用。
- SSE 每稳定采样逐客户端检查最近 15 秒有正常帧或慢读字节进展，允许最多 5 秒重连间隔；启动 15 秒和明确的续期移交 30 秒不计稳定样本。任何协议错误或稳定窗口违规均不能通过，合法 EOF/传输断开与主动重连单列。历史连接峰值不能代替持续验收。短自检只用 3 秒启动窗口，不代表长期证据。
- 短自检将 k6 工具的首次续期调度设为已到期，以实际登录、Cookie/Max-Age 和后续受权路由验证续期接线；不修改产品 TTL、数据库到期时间或系统时钟，不能当作已经运行 12 小时。
- 负载停止并关闭本轮 SSE 后仍保持冻结，60 秒 quiet recovery 验收队列/锁回基线、连接不超过基线+20、各 PID FD 不超过基线+10。缺必需采样标 blocked；RSS/CPU/heap/goroutine 权限或能力缺失明确 missing，不能据此宣称内存泄漏已排除。
- soak 必须新建 trial，连续真实 86400 秒 k6，最终 trial completed/1440 点/0 missing；记录两个组件实际起止与真实重叠。负载结束后最多等 120 秒 trial 完成，期间继续冻结检查。不能使用预先很久创建的 trial、拼接碎片或缩短时长。
