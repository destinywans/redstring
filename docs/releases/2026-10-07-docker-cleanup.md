# AIOps 本地 Docker 清理记录

2026-10-07（北京时间），WAN-117。用户明确授权清理本地 Docker 中无用的 AIOps 容器及镜像；当前 context 为 `desktop-linux`。

## 结果与依据

- 删除 26 个旧容器，其中 9 个原先运行或反复重启：暂停交付环境 6 个、目标服务已经停止的 exporter 3 个。其余为旧测试、SMTP 临时捕获及停用组件/数据库容器。
- 删除 21 个无保留容器消费者的项目/退役依赖镜像。Docker `system df` 镜像总量 246→225、占用 42.26→32.73 GB，显示减少约 9.53 GB；这不是 Docker Desktop 宿主稀疏磁盘已经缩小的证明。
- 保留当前 testlab 集合 11 个容器，另保留未证实闲置的 `aiops-test` 和 `aiops-canvas-demo` 实验/演示 Kind 节点，共 13 个 AIOps 容器。
- 两个当前宿主 AIOps API（18561/18562）仍有 6 条 TCP 连接到 testlab PG 15461。5173 前端及本机 Ollama 也在当前环境内；未把容器 Ollama 的退休误当作本机服务停止。
- 37 个无关容器的 ID、镜像、状态及启动时间全部与操作前一致；其他原镜像引用仍存在。全部 446 个 Docker 数据卷名称与操作前一致，未删除卷或绑定目录。

## 删除的容器

删除前逐一核对完整容器 ID、名称、镜像 ID 和状态；已退出候选若变成运行中会停止清理。对运行候选先正常 stop（20 秒期限），再精确 ID 执行 `docker rm`，未使用 `-v` 或全局 prune。delivery runner 近 30 分钟日志未显示接收新 job；交付功能此前保持暂停。

| 容器 | 删除前状态 | 原 ID |
| --- | --- | --- |
| `aiops-testlab-3cbde3095125-smtp-e0ef5bfb` | exited | `33d68efe9013` |
| `aiops-testlab-3cbde3095125-smtp-2525fc74` | exited | `9f6a93ab8540` |
| `aiops-v3-real-chain-pg` | exited | `98673e45a32b` |
| `wan121-template-tests-pg` | exited | `11594fe4c2ea` |
| `aiops-wan122-session-pg-20260929` | exited | `6598c76df063` |
| `aiops-test-alertmanager-1` | exited | `92d9ed010bce` |
| `aiops-test-observability-kafka-exporter-1` | restarting | `f2a6a0720192` |
| `aiops-test-observability-mysql-exporter-1` | running | `6dd5bebe79e7` |
| `aiops-test-observability-redis-exporter-1` | running | `e913f92f5d99` |
| `aiops-test-kafka-1` | exited | `2bceb616c778` |
| `aiops-test-prometheus-1` | exited | `cd6f1143bfdd` |
| `aiops-enterprise-otel-collector-1` | exited | `b8da21a2f806` |
| `aiops-enterprise-tempo-1` | exited | `4eb25893b110` |
| `aiops-enterprise-elasticsearch-1` | exited | `06b7d81de708` |
| `aiops-delivery-seaweedfs-1` | running | `532468b2b57f` |
| `aiops-delivery-runner-1` | running | `3602f53bc2af` |
| `aiops-delivery-control-plane` | running | `1d9823f30855` |
| `aiops-delivery-buildkit-1` | running | `f51f2a29e236` |
| `aiops-delivery-gitlab-1` | running | `aa6f562e26fc` |
| `aiops-delivery-registry-1` | running | `ee1214e62a5b` |
| `aiops-test-mysql-1` | exited | `3f23a55da541` |
| `aiops-test-grafana-1` | exited | `d85ba2162a85` |
| `aiops-test-loki-1` | exited | `2f34a1775c79` |
| `aiops-test-redis-1` | exited | `960c2a30267f` |
| `aiops-test-ollama-1` | exited | `913c0af69772` |
| `aiops-canvas-db-1` | exited | `8a13306420d1` |

## 删除的镜像

删除容器后重新检查所有保留容器的 image ID；下列 21 个镜像没有容器消费者，逐一删除而未使用 force。PG、Kind、GitLab、当前遥测组件等共享镜像随保留环境一起保留。

| 镜像引用 | 原 image ID |
| --- | --- |
| `aiops-test-api:c1a433ab0e518b38` | `10729c0bf90f` |
| `mysql:8.4` | `1bf26b4f970b` |
| `oliver006/redis_exporter:v1.67.0` | `2027fd6ab754` |
| `moby/buildkit@sha256:80b15f0735e87bab7bf59ec4d695dfb4a7cfb25521cf56dc75d6f256285b63ef` | `247d46d4889d` |
| `apache/kafka:4.3.1` | `33241f6eae1f` |
| `docker.elastic.co/elasticsearch/elasticsearch:8.17.4` | `430923108e45` |
| `registry@sha256:fd374bae807c225661adfe2c0c1f9970a0b8fab1761fd7dfb91e0fd9a8748f9b` | `5d28f1b4ff69` |
| `gitlab/gitlab-runner@sha256:0dfd45aafbd125c3d2ef31e1a57f585ac6eea32f69135cbf45af978700f6a5de` | `639716764b6f` |
| `prom/mysqld-exporter:v0.17.2` | `86e63a16bacb` |
| `danielqsj/kafka-exporter:v1.9.0` | `89568e5fcae9` |
| `postgres:17` | `97432f980da1` |
| `grafana/grafana:13.2.1` | `9a76b52cccaa` |
| `chrislusf/seaweedfs@sha256:ce9e796f1fe6f06968f4c04bdaf8f678dad9c8acdfef3d244133d71bfa6bf882` | `a4dfe9e6d63c` |
| `aiops-canvas-demo:local` | `a9dda79070a9` |
| `python:3.13.7-alpine3.22` | `b2236d9e1563` |
| `aiops-test-api:1409afa706c2144b` | `c1245173c06f` |
| `aiops-test-api:e49de7646cae8a17` | `c70266fe97cf` |
| `aiops-canvas-api:latest` | `d01bdd0a8d70` |
| `ollama/ollama:0.34.2` | `d772bbd3035a` |
| `otel/opentelemetry-collector-contrib:0.123.0` | `de13a5d7d2ec` |
| `redis:7.4-alpine` | `ec4f171e9ff0` |

## 保留的 AIOps 环境

当前 `.local/testlab/environment.json`、`component-environment.json` 和 `runtime-config.json` 仍登记 testlab PG、遥测、Vault、Keycloak、trial 来源；两个 API 对 PG 的实时连接明确存在。无 active 测试不等于这些依赖无用；已停止组件保持停止，没有为本轮清理启动服务。

| 保留容器 | 本次核对状态 |
| --- | --- |
| `aiops-backend-testlab-control-plane` | running |
| `aiops-testlab-3cbde3095125-gitlab` | running |
| `aiops-testlab-3cbde3095125-trial-prometheus` | exited |
| `aiops-testlab-3cbde3095125-keycloak` | exited |
| `aiops-testlab-3cbde3095125-otel` | exited |
| `aiops-testlab-3cbde3095125-tempo` | exited |
| `aiops-testlab-3cbde3095125-loki` | exited |
| `aiops-testlab-3cbde3095125-vault` | running |
| `aiops-testlab-3cbde3095125-alertmanager` | exited |
| `aiops-testlab-3cbde3095125-prometheus` | exited |
| `aiops-testlab-3cbde3095125-pg` | running |
| `aiops-test-control-plane` | running |
| `aiops-canvas-demo-control-plane` | running |

实验/演示 Kind 节点包含应用及持久状态，本轮未证明它们已退役，继续保留并记作后续环境整理项。当前 Docker 中 AIOps 仍有 6 个运行容器，分别为 testlab Kind/GitLab/Vault/PG 与上述两个实验节点。

## 数据保留与恢复边界

- 删除 `aiops-canvas-db-1` 仅退休旧容器；`aiops-canvas_database` 卷完整保留。未来使用主 compose 重建数据库时须明确复用原卷，不能把它当作已经清空的数据源。
- `aiops-test_*`、`aiops-enterprise_*`、`aiops-delivery_*` 命名卷及删除容器挂载的匿名卷保持。完整容器 ID、镜像 ID、卷名和挂载路径保存在私有 `.local/docker-cleanup-20261007/cleanup-plan.json`，不保存密码/token/DSN。
- 旧 delivery Kind 容器已经删除；恢复时需要明确选择重建或从保存的节点卷恢复。宿主 kubeconfig 的旧入口未自动改写，不能据它仍存在而认为集群仍运行。
- 原测试报告、源代码、绑定目录和 14 个未提交文件内容保持；本轮仅将新的计划/检查点前缀与本文提交，不混入测试候选。
- 全局 build cache、其他项目镜像与数据卷没有被清理；磁盘统计不被作为产品功能验收。

## 验证与下一步

精确回读确认 26 个目标容器与 21 个目标镜像已不存在，所有保留容器和镜像可访问、37 个无关容器未变、446 个卷未丢失、原未提交文件指纹一致；两个 API 的监听和数据库连接保持。本轮无产品源码修改、测试、迁移或部署。

后续环境整理先明确两个实验/演示集群及保留历史卷的用途，再按确认范围退役；不通过全局 prune 代替归属核对。当前环境元数据仍保留旧 delivery/组件信息，需要实际恢复或继续退役时再更新对应记录。
