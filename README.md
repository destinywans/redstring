<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/brand/redstring-mark-on-dark.svg">
  <img src="docs/brand/redstring-mark.svg" width="56" height="56" alt="Redstring">
</picture>

# Redstring

Redstring 是一个可自托管的 AI 运维工作区。团队可以从告警发起调查，在画布上分支验证故障假设，并基于证据审批恢复操作。

[在线演示](https://redstring.wans.run/)（示例数据） · [本地试用](#快速开始) · [产品介绍](https://wans.run/redstring/)

![Redstring 调查画布中的故障分支与调查结论](docs/images/canvas-investigation.png)

<sub>截图来自在线演示，使用示例数据。</sub>

## 核心工作流

以一次新版本发布后 HTTP 5xx 比例升高为例。

1. **从告警发起调查**。值班人员确认受影响的服务、环境和告警时间，从告警组或事件创建调查画布。
2. **按假设分支排查**。分别调查新版本行为、下游依赖和资源压力。每条分支继承父分支的一版上下文，可以独立追问、取证并记录结论。
3. **核对证据并回写结论**。AI 使用只读工具查询已接入的指标、脱敏日志和 Runbook。调查者可以追溯证据来源、对照分支结果，再通过合并预览选择要写回主线的结论或证据。
4. **审批回滚**。发起人核对已登记的 Kubernetes Deployment 和目标修订，提交回滚提案。发起人之外的两位授权成员用通行密钥批准同一份提案，再由独立执行器执行回滚。
5. **验证恢复**。执行器等待滚动更新完成，再在观察窗口内检查请求率、错误率和样本时效。团队可以在执行详情查看进度、采样记录和验证结果，并据此准备复盘。

## 工作区

- **对话与画布**。可以在对话里查询某个服务的指标和日志，也可以用画布处理多条排查路径，让每条分支保留各自的上下文和证据。
- **服务与观测**。按应用和环境查看服务目录、资源依赖和 SLO 信号。结合指标趋势、日志样本和调用链路，查看同一时段的服务表现。
- **告警与事件**。认领告警、跟进事件状态。在事件详情查看告警前后的 GitLab 部署和 Kubernetes 修订，作为排查线索。
- **变更与执行**。查看已接入的部署记录和提交差异，核对可回滚的修订。执行详情保留审批、滚动进度和恢复验证结果。
- **Runbook 与复盘**。维护、审核和检索排查手册，为已解决的事件记录影响、根因和行动项。已发布的复盘可以作为新 Runbook 的起点。
- **集成与权限**。可接入 Kubernetes、Prometheus、Loki、Tempo、Alertmanager 和 GitLab。按环境配置数据源和模型，按角色及应用范围授权。

## 快速开始

在本地预览示例工作区，只需 Git、Node.js、pnpm 和 `make`。

```sh
git clone https://github.com/destinywans/redstring.git
cd redstring
pnpm --dir web install --frozen-lockfile
make web-dev
```

打开 [http://127.0.0.1:5173](http://127.0.0.1:5173)。要启动后端并接入自己的测试数据，请按[自托管指南](docs/getting-started.md#本机完整运行最小实例)配置。

## 文档

[架构说明](docs/architecture.md) · [API 参考](docs/api-contract.md)

## 安全须知

Beta 版本仅供测试环境试用，请勿暴露到公网。配置了写权限的模块可以修改外部系统，请只使用测试凭据。AI 取证会将选用的证据发送给你配置的模型。

## 反馈

[提交缺陷](https://github.com/destinywans/redstring/issues/new/choose) · [私密报告安全漏洞](SECURITY.md)

## 许可证

[Apache-2.0](LICENSE) · [第三方许可](THIRD_PARTY_NOTICES.md)
