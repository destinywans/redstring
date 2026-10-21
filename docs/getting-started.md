# 运行与部署

README 的「快速开始」只列命令，这里补充各方式的前提、地址约定和注意事项。

## 准备

| 工具 | 说明 |
| --- | --- |
| Go | 版本以 `go.mod` 为准（1.27.1） |
| Node 与 pnpm | pnpm 版本以 `web/package.json` 的 `packageManager` 为准（12.4.1） |
| Docker Compose | 用 `docker compose` 子命令，只用来起 PostgreSQL（pgvector） |
| openssl | `make init` 用它生成随机密码 |

本机开发会用到这些端口，都只监听 `127.0.0.1`：

| 端口 | 用途 | 能否改 |
| --- | --- | --- |
| 5173 | Vite 开发服务器 | 示例数据模式可用 `pnpm --dir web dev --port <端口>`；真实 API 模式下 `make web-api` 只放行 `http://127.0.0.1:5173`，需要先腾出这个端口 |
| 18080 | `make web-api` 的 API | 固定，Vite 代理也指向它 |
| 8080 | `make run` | `.env` 的 `AIOPS_ADDR` |
| 15432 | PostgreSQL | `.env` 的 `POSTGRES_PORT`，同时改 `DATABASE_URL` 里的端口 |

Vite 开了 `strictPort`，5173 被占用时直接报 `Port 5173 is already in use` 退出，不会换端口。

`compose.yaml` 固定了项目名 `aiops-canvas`，数据库卷叫 `aiops-canvas_database`。如果这台机器上已经有同名项目（例如旧 checkout 跑过 `make db`），新的 `make db` 会接着用旧卷：PostgreSQL 只在空卷上初始化，旧数据和旧密码都保留，新 `.env` 的密码连不上。这时在 `.env` 里加一行 `COMPOSE_PROJECT_NAME=<新名字>`，`make db` 和之后的 `docker compose` 命令就会用新的项目和卷。

2026-10-19 在 main `9d19f21e` 的干净 clone 上验证过：示例数据模式、最小实例（Vite 和 Go 托管两种方式）、`go build` 与 `pnpm --dir web build`。环境为 macOS arm64、Go 1.27.1、Node 26.11.0、pnpm 12.4.1、Docker Compose 5.6.0。Docker Compose 的 `app` profile 没有实跑。打 tag 前又在 `94f8933d` 的干净 clone 上重验：`go build`（`vcs.modified=false`）与 `pnpm --dir web build` 通过；按 README 的快速开始起最小实例，Vite 开发方式（`make web-api`）和 Go 托管（`make run`）都能登录，实例描述、工作区品牌（Redstring）、服务、首页接口正常；`OLLAMA_MODEL=` 时模型列表为空，`make init` 默认值时模型目录列出 `qwen3.6:latest`。这次没有逐页浏览界面。

## 两种前端模式

前端在构建时决定数据来源：

| 模式 | 怎么启用 | 数据 |
| --- | --- | --- |
| 示例数据（默认） | `make web-dev`、`pnpm --dir web build` | 前端 Mock，顶栏标注“示意数据”，不需要后端 |
| 真实 API | 设 `VITE_API_MODE=http`；`make web-build` 和 Dockerfile 已默认设置 | 走 `/api/v1`，需要后端和 PostgreSQL |

由 Go 托管或用 Docker 交付时，产物必须是真实 API 模式，否则页面只显示示例数据。在线演示用的是示例数据模式。

公开演示构建时可再设 `VITE_DEMO_ABOUT_URL`（英文页另设 `VITE_DEMO_ABOUT_URL_EN`），顶栏的「示意数据」标记就会链到这个项目介绍页，在新标签页打开。不设时它只是一个文字标记。

要把演示嵌在别的页面里时，构建时设 `VITE_EMBED_ORIGINS`（逗号分隔的来源，例如 `https://wans.run`）：嵌入页可以在地址里带 `?theme=light|dark|system&lang=zh|en` 决定打开时的主题和语言，打开后再用 `postMessage` 发 `{ type: "redstring:preferences", theme, lang }` 跟着它切换；只有列出的来源会被接受。这些只作用于本次访问，不写进访客保存的偏好。地址参数不依赖这个变量，任何构建都认。能不能被别的站点嵌入由服务器的 `frame-ancestors` 决定，不在前端。

## 只看界面

```sh
pnpm --dir web install --frozen-lockfile
make web-dev
```

浏览器打开 **http://127.0.0.1:5173**。常用入口见 [前端本地预览](web-dev.md)。旧版界面 `/legacy` 不走示例数据，只开前端时它的登录页会显示“请求失败 (502)”，要看旧版需要按下一节启动后端。

## 本机完整运行（最小实例）

最小实例是空库、不接集群、不接模型的真实 API 模式：能登录，各页如实显示空状态或“未接入”。

首次准备：

```sh
make init
mkdir -p .local
cp docs/examples/v3-instance.nonprod.json .local/v3-instance.json
cat >> .env <<EOF
AIOPS_V3_INSTANCE_CONFIG=$PWD/.local/v3-instance.json
OLLAMA_MODEL=
EOF
make db
pnpm --dir web install --frozen-lockfile
```

- `make init` 生成权限为 0600 的 `.env`，包含随机数据库与初始管理员密码，不覆盖已有文件。管理员邮箱默认 `admin@localhost`，密码是 `.env` 里 `AIOPS_ADMIN_PASSWORD` 的值。不要提交或分享该文件。
- `AIOPS_V3_INSTANCE_CONFIG` 指向实例描述（实例名、域名、环境列表），必须设。不设时服务以旧版模式启动，当前界面依赖的 `/api/v1/instance` 返回 404，服务目录、成员等页面会一直停在加载骨架；旧版模式只供 `/legacy` 使用。示例 `v3-instance.nonprod.json` 含 dev、test、staging 三个非生产环境，域名是占位值，本机运行不需要改。
- 数据库第一次以这个配置启动后就绑定该实例，之后漏设变量会拒绝启动（`database is bound to a V3 instance`）。以旧版模式启动过的库，补上变量后可以直接切过来。
- 追加的 `OLLAMA_MODEL=` 覆盖 `make init` 写的本机 Ollama 模型，表示先不接模型；直接改原来那一行也一样。本机已经装好 Ollama 的话可以不加这一行，见下面的“模型”。

两个终端分别运行：

```sh
# 终端 1：API，加载 .env 后固定开发地址与允许的浏览器 Origin
make web-api
```

```sh
# 终端 2：前端，走真实 API
VITE_API_MODE=http make web-dev
```

浏览器打开 **http://127.0.0.1:5173** 并登录。Vite 将 `/api`、`/healthz`、`/openapi.json` 代理到 `127.0.0.1:18080`。`make web-api` 在加载 `.env` 后设置 `AIOPS_ADDR=127.0.0.1:18080`、`AIOPS_PUBLIC_ORIGIN=http://127.0.0.1:5173`；保持这组地址一致，避免写请求被同源检查拒绝。每个终端用 Ctrl+C 停止对应进程。

登录后顶栏环境是 Development。服务、告警、事件、Runbook 等列表为空；日志、链路、通知路由等显示“未接入”，从集成中心接入。「集成中心 › 模型」管理页还需要环境模型绑定（`AIOPS_V3_MODEL_BINDINGS_FILE`，格式见 [模型绑定示例](examples/v3-model-bindings.md)），没配时显示“这个环境还没有绑定模型”（管理员还能看到 `503 model_binding_unavailable`）；对话里能选哪些模型不受它影响。静默与抑制页在没接 Alertmanager 时同样显示“暂时读不到”。

### 模型

模型按 `.env` 显式配置，项目不会自动下载模型；支持的适配器与配置项见 [模型说明](../internal/inference/README.md)。

`make init` 写入的是本机 Ollama 的 `OLLAMA_MODEL=qwen3.6:latest`，并设 `OLLAMA_API=chat_completions`、`OLLAMA_BASE_URL=http://127.0.0.1:11434/v1`，走 Ollama 的 OpenAI 兼容接口。本机没有 Ollama 或没拉这个模型时，模型目录照样列出它，但发出的消息会显示“模型回复失败”。不要去掉 `OLLAMA_API`：当前界面的模型目录不支持原生 Ollama 协议，那样 `/api/v1/models` 会整体返回 `503 model_catalog_unavailable`，所有 AI 输入框的模型位只显示“…”。按需要选一种：

| 想要 | `.env` 怎么写 | 界面表现 |
| --- | --- | --- |
| 先不接模型 | `OLLAMA_MODEL=` | 模型列表为空，选择器提示“没有匹配的模型”；发送时提示没有可用模型 |
| 本机 Ollama | `make init` 的默认值，不用改；先 `ollama pull qwen3.6:latest` 和 `ollama pull bge-m3`（Embedding，缺了向量索引会失败、关键词检索照常），换模型只改 `OLLAMA_MODEL` | 走 Ollama 的 OpenAI 兼容接口，模型显示“已配置；连接状态未验证” |
| DeepSeek 等 OpenAI 兼容接口 | 按 `.env.example` 的 `OPENAI_*` 注释配置 | 同上 |

2026-10-19 只验证了前两行的模型目录和空模型时的提示（第二行当时是手工加的同一组配置，`make init` 默认值后来改成了它），没有用真实模型生成过回复。

## 构建后由 Go 托管

```sh
make web-build
AIOPS_WEB_DIR="$PWD/web/dist" make run
```

默认访问 **http://127.0.0.1:8080**；地址以 `.env` 的 `AIOPS_ADDR` 为准。`make run` 同样加载 `.env`，上面写入的 `AIOPS_V3_INSTANCE_CONFIG` 照样生效；本机用这个地址访问时不需要设 `AIOPS_PUBLIC_ORIGIN`。`.env.example` 中 Web 目录与生产 Origin 仅作为注释，避免覆盖命令行传入值。如自己的 `.env` 已定义同名变量，启动脚本会以该文件值为准，需同步修改。未设置 `AIOPS_WEB_DIR` 时，`make run` 只提供 API，不托管 Web 页面。

API 前缀 `/api/v1`；契约入口 `/openapi.json`，健康入口 `/healthz`。

## 构建检查

```sh
go build -o bin/aiops ./cmd/aiops
pnpm --dir web build
```

不加 `-o` 时 `go build ./cmd/aiops` 会在仓库根目录留下约 60 MB 的 `aiops`（已在 `.gitignore` 里），建议用上面的 `-o bin/aiops`。前端构建会提示个别 chunk 超过 500 kB，这是已知警告，不影响产物。

## Docker Compose

**用 Docker Compose 运行整个应用目前不可用**（原因见本节末尾），请用前两节的宿主机方式；数据库照常由 `make db` 起。下面的命令只作参考：

```sh
docker compose --profile app up -d --build
```

数据保存在独立 volume，`docker compose down` 默认保留；不要随意加 `-v`。服务器部署需配置 HTTPS 反向代理、实际 `AIOPS_PUBLIC_ORIGIN` 与 `AIOPS_SECURE_COOKIES=true`，并把代理地址写进 `AIOPS_TRUSTED_PROXY_CIDRS`（否则登录限流按代理 IP 共用）。模型和集群凭据保留在服务端；本地默认端口仅映射回环。

容器方式没有做过端到端验收。按 `compose.yaml` 现状，`api` 服务没有传 `AIOPS_V3_INSTANCE_CONFIG`，也没有挂载实例描述，会以旧版模式启动，当前界面的页面会停在加载骨架（同上）；它还总是带原生 Ollama 模型（`.env` 里留空也会回落到 `qwen3.6:latest`）。要跑当前界面，请用前两节的宿主机方式。2026-10 的验收实例是宿主机上的 Go 服务托管真实 API 模式的 Web 产物，前面加 TLS 代理，见 [验收计划](plans/2026-10-09-production-acceptance.md)。

## 故障演示环境

可复现的故障与回滚演示使用专用集群，见 [演示说明](demo.md)。不要在其他集群上运行这些脚本。
