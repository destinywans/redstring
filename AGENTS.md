# Redstring 贡献约定

写给在本仓库里改代码的人和 AI 编码代理。产品介绍和快速开始见 [README](README.md)，领域用语见 [CONTEXT.md](CONTEXT.md)。

## 项目结构

- `cmd/aiops`：产品入口，服务端和命令行子命令都在这里。仓库只有一个 Go 模块。
- `internal/`：后端模块。PostgreSQL 里的消息、证据、版本和事件是唯一的权威历史；ADK Session 只保存单次 Attempt 的执行状态。业务仓储、向量、Embedding、模型和连接器分别适配，换实现不改写历史。
- `internal/apicontract/openapi.json`：机器可读的 API 契约（生成产物，见下文）。
- `migrations/`：按序号命名的 SQL 迁移，嵌入二进制，服务启动时执行。
- `web/`：前端（React、TypeScript、Vite）。默认界面在 `web/src/v3`，旧界面保留在 `/legacy`。
- `scripts/`：本地启动、契约生成与检查脚本。
- `demo/`：故障演示用的示例服务、遥测和实验命令行，见 `docs/demo.md`。
- `tools/testlab`、`tools/acceptance`：作者实验环境里用的测试与验收工具，依赖作者本机的集群和服务，普通贡献不需要运行。
- `docs/`：契约与运行说明。`docs/plans/`、`docs/releases/` 是作者的计划与验收记录，不是用户文档。

## 构建与校验

需要 Go 1.27.1、Node 与 pnpm（版本固定在 `web/package.json` 的 `packageManager`，可用 `corepack enable` 自动切换）、Docker Compose（本地 PostgreSQL + pgvector）。

```sh
go build ./...
go vet ./...
go test -race ./internal/<受影响的包>/...
pnpm --dir web install --frozen-lockfile
pnpm --dir web build            # tsc -b + vite build
```

- 只跑受影响的包和检查即可，提交说明里写清跑了哪些。
- 需要数据库的测试读 `TEST_DATABASE_URL`，每个测试套件只建、删自己随机命名的 schema。全量校验：先 `make init db`（生成 `.env` 并启动本地数据库），再 `./scripts/verify.sh --postgres`（`go test -race ./...`、`go vet ./...` 和 `check-contract.py`；下文的契约生成比对和 `check-v3-contract.py` 不在其中，改了路由要另外跑）。
- 真实模型、真实 Kubernetes 的冒烟测试是单独的证据，不在默认测试里。

## API 契约

- 语义说明在 `docs/api-contract.md`，V3 接口在 `docs/api/`。`internal/apicontract/openapi.json` 由 `scripts/build-openapi.py` 生成：它依次执行引入的 `scripts/*-openapi.py`（V3 片段是 `scripts/v3-openapi.py` 和 `scripts/v3-openapi-*.py`），再合并手写的 `internal/v3security/openapi.json`。不要手改生成的 JSON。
- 新增、删除或修改路由时，在同一个提交里更新生成脚本、重新生成契约，并跑下面的检查。生成成功不等于校验通过。

```sh
python3 scripts/build-openapi.py
git diff --exit-code internal/apicontract/openapi.json   # 提交的契约是最新的
python3 scripts/check-contract.py                        # 引用有效，与源码声明的路由一致
python3 scripts/check-v3-contract.py                     # docs/api/v3-contract.md 与前端 mock 路由一致
```

## 数据库迁移

- 数据结构变化写成 `migrations/` 里的新迁移（用下一个序号），或者放进对应模块的 `Migrate`。
- `migrations/` 的文件按文件名记录在 `schema_migrations` 表里：已经在某个库上执行过的文件，改内容不会再执行，所以结构变化要用新文件表达。
- 模块自己的 `Migrate`（例如 `internal/knowledge`、`internal/operations`）不记账，每次启动都整段执行，语句必须幂等（`CREATE TABLE IF NOT EXISTS`、`ADD COLUMN IF NOT EXISTS` 等）。
- 提交或 PR 说明里写明迁移有没有在本地数据库上实际执行过。

## 产品原则

改动涉及模型、工具或执行路径时，先确认没有违反这几条：

- **模型只读取证。** 给模型用的工具只能读取；模型的猜测不是证据，结论要能追到具体的工具调用、检索结果或 Trace。
- **写动作必须经过明确提案、人工批准和独立执行器。** 任何会改变外部系统的操作（Kubernetes、GitLab、告警规则、配置等）先形成一份冻结的具体提案，由有权限的人批准，再交给与 Agent 隔离的执行器执行并记录结果。结果不明时先核对实际状态，不盲目重试。上下文继承和分支合并不继承授权。
- **授权在服务端。** 权限和同源校验都在服务端完成，前端隐藏按钮不算权限控制。
- **不冒充真实数据。** 示例和演示数据要标明；缺少真实适配时显示“待接入”或“未知”，不要编造状态。
- 界面支持中英文和深浅色，改界面文案时两种语言都要更新。

## 凭据与本地文件

- 不提交 `.env`、`.local/`、IDE 配置、运行会话、kubeconfig 或任何凭据。配置示例只放占位符（见 `.env.example`）。
- 测试里的假凭据用一眼能看出是假的值，例如 `example.invalid` 域名、`synthetic-*`、`test-*`。
- 个人或本机的代理指令放在不进仓库的文件里，`.gitignore` 已经列出 `CLAUDE.local.md`、`AGENTS.override.md`。注意：Claude Code（2.1.277 起直接读取本文件）发现 `CLAUDE.local.md` 后默认不再读取本文件，需要在里面写一行 `@AGENTS.md` 导入；Codex 的 `AGENTS.override.md` 会代替本文件，需要把本文件的内容一并放进去，本文件更新后要重新同步。
