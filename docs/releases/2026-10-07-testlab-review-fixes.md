# WAN-138：测试证据与首失败保留修复

日期：2026-10-07。用户在代码复审后确认修复两个测试工具问题。
分支：`codex/wan-138-backend-testlab`；修复前 HEAD：`7f8c800ad2ef927b45cd9e55813b4b19bc9d8ca8`。
需求和交付记录：[WAN-138](https://linear.app/wansss/issue/WAN-138)。

## 修复行为

- 角色详情的 DTO、版本、缓存头和跨实例一致性校验在共享 HTTP 调用记录成功前执行。校验失败仍保留实际 HTTP 200，接口记录标为 failed；该失败记录不能被看板计作已验证的 boundary。
- 对象读取的业务断言和 ACL 恢复同时失败时，辅助诊断写盘异常不再覆盖原业务异常。外层执行器继续保存首失败并执行一次清理。

改动仅涉及两个工具及各自测试。它们在本次开始时是未跟踪文件，本次将修复后的完整文件纳入版本控制；其共享依赖已在基线中。其余未提交验收计划、查询工具和候选保持原状态。产品 Go、共享 helper、ACL、预算和证据资格未修改。

## 验证

修复前新增角色回归为 4 个测试、6 个失败子项：错误 notes/matrix/revision/cache 未在 HTTP 成功记录前拒绝、看板 boundary 误绿、恢复响应误记成功。有效响应与调用方 validator 的正向测试通过。

对象新增回归在旧实现的“业务断言失败 + 恢复失败 + 辅助写盘失败”组合报 OSError，覆盖原账本断言。修复后覆盖四种异常组合，验证实际 `exercise → execute → first_red` 路径、双份首失败 JSON、原错误、一次清理和停止恢复请求。

最终定向命令：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest \
  tools.testlab.tests.test_api_role_read_storage_failure \
  tools.testlab.tests.test_api_object_reads_storage_failure \
  tools.testlab.tests.test_overview
```

结果：**92 tests passed**（角色 24、对象 15、看板 53）。其中看板反例使用真实 `actual_interface`、临时 SQLite Store 和 `get_overview`，HTTP 传输为离线替身。四个 Python 文件 AST 解析通过，限定依赖检查无未跟踪依赖，diff 空白检查通过。

## 验收边界

本轮没有执行真实 HTTP、PG 故障注入、性能、24h 或前端验证，没有覆盖历史原始证据，也不更新已有 native 运行的源码资格。角色工具仍属于准备候选；后续 native 验收必须重新核对并冻结 helper hashes。产品与接口整体验收状态保持开放。

提交、远端回读结果追加到同一 Linear 主题；不合并、不部署。
