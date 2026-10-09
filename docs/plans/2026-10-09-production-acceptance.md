# 生产验收：前后端联调与全量回归（2026-10-09）

2026-10-09 用户确认：新版前端（v3）从未接过真实后端，需要以“生产可用”为目标做完整验收。

- **本轮范围**：功能、权限、故障；性能压测和连续 24 小时运行后置。
- **处理方式**：边测边修，界面改动先出设计图。
- **大模型**：DeepSeek。
- **写操作**：允许在 aiops-test 的三台 Lima 虚拟机 Kubernetes 上做写操作和受控故障注入。

缺陷与修复记录见[交付记录](../releases/2026-10-09-production-acceptance.md)。

## 验收环境

| 部分 | 组成 | 入口 |
| --- | --- | --- |
| 实验数据底座 | aiops-test：三台 Lima 虚拟机 kubeadm（`aiops-vm-cluster`），order/checkout/message 各 2 副本加低速流量；MySQL、Redis、Kafka；Prometheus、Loki、Alertmanager、Grafana；ES、Tempo、OTel；GitLab | aiops-test 仓库的 `cluster/cluster.py`、`enterprise/manage.py`、Compose |
| 生产级实例 | tier=production，环境 `prod` 对应实验集群；独立库 `redstring_prod` | `https://prod.redstring.test:18443` |
| 非生产实例 | tier=nonprod，环境 dev/test/staging，其中 test 对应实验集群；独立库 `redstring_nonprod` | `https://nonprod.redstring.test:18444` |
| 数据库 | 独立容器 `aiops-acceptance-pg`（pgvector 0.8.6-pg17），从零跑全部迁移 | 127.0.0.1:25432 |
| TLS | nginx 容器 `aiops-acceptance-proxy`，本地自签证书，流式响应不缓冲 | 18443 / 18444 |
| 模型 | DeepSeek `deepseek-flash`（默认）、`deepseek-v4-pro`；向量模型用实验 Ollama 的 `bge-m3:567m` | 两个实例的模型绑定 |

**不改动的东西**
- 通行密钥要求 HTTPS 和真实 RP ID，所以两个实例都放在 TLS 代理后面。
- 域名解析用浏览器的 `--host-resolver-rules`，证书信任用 `--ignore-certificate-errors-spki-list`：不改 `/etc/hosts`，也不改系统钥匙串。
- 凭据、配置、日志全部放在 `.local/acceptance/`，不提交。

```sh
python3 tools/acceptance/acceptance.py tls            # 证书与 TLS 代理
python3 tools/acceptance/acceptance.py start          # 两个实例（prepare 会重写配置）
python3 tools/acceptance/acceptance.py status
pnpm --dir web e2e                                     # Playwright 全部用例
python3 tools/acceptance/crawl-summary.py              # 全页面对拍汇总
```

## 通过标准

页面能打开不等于通过。每条用例都按以下标准判定：

1. **页面层**：没有未捕获异常，不白屏，控制台没有错误。所有 `/api/v1` 调用要么成功，要么返回合同里定义的明确状态，并且界面给出对应提示。
2. **数据层**：页面上的数字和状态，要和实验数据源（Prometheus、K8s、Loki、Tempo、ES、GitLab、Alertmanager）的实际值一致；写操作刷新后仍然存在，并且审计里查得到。
3. **权限层**：每个内置角色只能看到、做到它被授权的事。越权请求返回 403/404，界面隐藏或禁用对应入口。
4. **环境层**：生产实例只有 `prod`。非生产实例的 `all` 视图只读，写操作返回 `409 env_view_only`。不同实例的数据互不可见。
5. **异常层**：数据源、模型、数据库或后端不可用时，界面给出明确提示，不卡死、不白屏；恢复后能自动回到正常。
6. **AI 层**：模型只读取证。写操作必须经过“提案 → 通行密钥批准 → 独立执行器”，每一步都留下审计。

## 结果分类

| 分类 | 含义 | 是否计入通过 |
| --- | --- | --- |
| 通过 | 满足上面全部标准 | 是 |
| 缺陷 P0 | 阻断主流程或数据错误、越权、白屏 | 否，当场修 |
| 缺陷 P1 | 主要功能不可用或结果明显错误，但有绕行办法 | 否，当场修 |
| 缺陷 P2 | 次要功能问题、提示不清、局部显示错误 | 否，本轮尽量修 |
| 缺陷 P3 | 瑕疵、效率、文案 | 否，登记 |
| 缺来源 | 后端没有可信的数据来源（如原有 38 条），明确返回 503 或未知 | 不算缺陷，也不算通过，单独列出 |
| 缺能力 | 产品还没有这项能力（如 v3 执行 Kubernetes 写操作） | 不算通过；需要用户确认设计后再实现 |

## 阶段

| 阶段 | 内容 | 结束条件 |
| --- | --- | --- |
| 0 | 环境、账号、数据接入 | 两个实例可用；实验数据源经集成中心登记成功 |
| 1 | 全页面对拍：54 个静态路由 × 2 个实例 × 中英文、深浅色，加上有数据后的详情页 | 汇总里没有未分类的失败 |
| 2 | 12 条黄金路径端到端 | 全部通过，或明确登记为缺来源、缺能力 |
| 3 | 权限矩阵、环境隔离、故障与恢复、输入边界 | 同上 |
| 4 | 实验集群故障演练（5 个场景） | 每个场景走完“注入 → 告警 → 调查 → 处置 → 验证 → 复盘” |
| 5 | 修复后全量回归 | 阶段 1～4 一次性完整重跑 |

没有依赖关系的部分并行执行：Playwright 多 worker 并发、两个实例同时测、环境准备和用例编写交替进行。
