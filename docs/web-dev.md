# 前端本地预览（v3）

v3 前端默认使用前端 Mock 数据，**不需要启动后端或数据库**。

## 启动

在仓库根目录运行：

```sh
make web-dev
```

等同于 `pnpm --dir web dev`。第一次运行或依赖变更后，先安装依赖：

```sh
pnpm --dir web install
```

启动后用浏览器打开 **http://127.0.0.1:5173**，按 Ctrl+C 停止。

## 常用入口

| 页面 | 地址 |
| --- | --- |
| 总览 | http://127.0.0.1:5173/ |
| Canvas 调查 | http://127.0.0.1:5173/canvas/INV-231 |
| 收件箱 | http://127.0.0.1:5173/inbox |
| 服务目录 | http://127.0.0.1:5173/services |
| 集成中心 | http://127.0.0.1:5173/integrations |
| Agent 运行台 | http://127.0.0.1:5173/agents/lifecycle |
| 偏好设置（主题、语言） | http://127.0.0.1:5173/settings/preferences |
| 旧版界面 | http://127.0.0.1:5173/legacy |

页面顶栏的“示意数据”标记表示当前是 Mock 数据，不代表真实业务状态。

## 连接真实后端（后端按契约实现后）

```sh
# 终端 1：API
make web-api
```

```sh
# 终端 2：前端改走 /api/v1
VITE_API_MODE=http pnpm --dir web dev
```

接口契约见 [docs/api/v3-contract.md](api/v3-contract.md)。

## 构建检查

```sh
make web-build
```
