# 运行与部署

README 的「快速开始」只列命令，这里补充各方式的前提、地址约定和注意事项。

## 两种前端模式

前端在构建时决定数据来源：

| 模式 | 怎么启用 | 数据 |
| --- | --- | --- |
| 示例数据（默认） | `make web-dev`、`pnpm --dir web build` | 前端 Mock，顶栏标注“示意数据”，不需要后端 |
| 真实 API | 设 `VITE_API_MODE=http`；`make web-build` 和 Dockerfile 已默认设置 | 走 `/api/v1`，需要后端和 PostgreSQL |

由 Go 托管或用 Docker 交付时，产物必须是真实 API 模式，否则页面只显示示例数据。在线演示用的是示例数据模式。

## 只看界面

```sh
pnpm --dir web install --frozen-lockfile
make web-dev
```

浏览器打开 **http://127.0.0.1:5173**。常用入口见 [前端本地预览](web-dev.md)；旧版界面在 `/legacy`。

## 本机完整运行

准备 Go、Node、pnpm 与 Docker Compose。模型按 `.env` 显式配置，项目不会自动下载模型；支持的适配器与配置项见 [模型说明](../internal/inference/README.md)。

首次准备：

```sh
make init db
pnpm --dir web install --frozen-lockfile
```

`make init` 生成权限为 0600 的 `.env`，包含随机数据库与初始管理员密码，不覆盖已有文件。管理员邮箱默认 `admin@localhost`，密码在本机 `.env` 中查看。不要提交或分享该文件。

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

## 构建后由 Go 托管

```sh
make web-build
AIOPS_WEB_DIR="$PWD/web/dist" make run
```

默认访问 **http://127.0.0.1:8080**；地址以 `.env` 的 `AIOPS_ADDR` 为准。`.env.example` 中 Web 目录与生产 Origin 仅作为注释，避免覆盖命令行传入值。如自己的 `.env` 已定义同名变量，启动脚本会以该文件值为准，需同步修改。未设置 `AIOPS_WEB_DIR` 时，`make run` 只提供 API，不托管 Web 页面。

API 前缀 `/api/v1`；契约入口 `/openapi.json`，健康入口 `/healthz`。

## Docker Compose

```sh
docker compose --profile app up -d --build
```

数据保存在独立 volume，`docker compose down` 默认保留；不要随意加 `-v`。服务器部署需配置 HTTPS 反向代理、实际 `AIOPS_PUBLIC_ORIGIN` 与 `AIOPS_SECURE_COOKIES=true`。模型和集群凭据保留在服务端；本地默认端口仅映射回环。

容器方式没有做过端到端验收。2026-10 的验收实例是宿主机上的 Go 服务托管真实 API 模式的 Web 产物，前面加 TLS 代理，见 [验收计划](plans/2026-10-09-production-acceptance.md)。

## 故障演示环境

可复现的故障与回滚演示使用专用集群，见 [演示说明](demo.md)。不要在其他集群上运行这些脚本。
