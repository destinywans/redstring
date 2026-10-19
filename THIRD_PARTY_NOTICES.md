# 第三方内容声明 / Third-party notices

Redstring 自己的源码按 [Apache-2.0](LICENSE) 授权。下面这些随仓库一起分发的第三方内容**不受 Apache-2.0 覆盖**，各自按原许可使用。fork 或再分发本项目时，请一并保留这些声明，并自行确认符合各自的许可条件。

Redstring's own source code is licensed under [Apache-2.0](LICENSE). The third-party content below ships with this repository but is **not** covered by Apache-2.0; each item keeps its own license. Keep these notices when you fork or redistribute the project, and check that your use meets each license.

## 1. MiSans 字体（只在 v0.1.0 之前的提交里）/ MiSans font (only in commits before v0.1.0)

当前版本不包含任何字体文件，界面使用系统字体。v0.1.0 之前的提交里有 MiSans 可变字体 `web/src/assets/fonts/MiSansVF.woff2` 和许可全文 `web/public/fonts/MiSans-LICENSE.pdf`（Copyright © 2020-2025 Beijing Xiaomi Mobile Software Co.,Ltd. All Rights Reserved.）。这些文件仍在 git 历史里，不受 Apache-2.0 覆盖，按《MiSans 字体知识产权许可协议》使用：协议允许分发用 MiSans 创作的应用，但不允许改编字体，也不允许把字体或其副本单独出租、再许可、赠与、出借、再分发或出售。**不要从历史提交里取出字体单独使用或分发**；需要 MiSans 请从[小米官方](https://hyperos.mi.com/font/zh/)获取并自行确认许可。

The current version ships no font files; the interface uses system fonts. Commits before v0.1.0 contain the MiSans variable font `web/src/assets/fonts/MiSansVF.woff2` and its license `web/public/fonts/MiSans-LICENSE.pdf` (Copyright © 2020-2025 Beijing Xiaomi Mobile Software Co.,Ltd. All Rights Reserved.). They remain in the git history, are not covered by Apache-2.0 and stay under the MiSans Font Intellectual Property License Agreement, which allows distributing applications made with MiSans but forbids adapting the font and renting, sublicensing, giving, lending, redistributing or selling the font or its copies on their own. **Do not extract the font from old commits for separate use or distribution**; get MiSans from [Xiaomi](https://hyperos.mi.com/font/zh/) and check its license yourself.

## 2. 拼音词典 / Pinyin dictionary（go-pinyin）

| 项 | 内容 |
| --- | --- |
| 文件 | `shared/search/pinyin.json`，以及由同一数据生成的 `migrations/019_search_pinyin.sql`；浏览器、Go 内存搜索和 PostgreSQL 检索共用 |
| 来源 | [go-pinyin v0.21.0](https://github.com/mozillazg/go-pinyin/tree/v0.21.0) 的完整字符词典（41,923 字），每字只保留首个无声调读音；生成器为 `scripts/generate-search-dictionary.go` |
| 版权与许可 | Copyright (c) 2016 mozillazg，MIT License，全文：[`shared/search/LICENSE.go-pinyin`](shared/search/LICENSE.go-pinyin) |

`pinyin.json` 经 `go:embed` 编进 Go 二进制（`shared/search/data.go`），也被前端打进 `web/dist` 产物（`web/src/lib/search.ts`）。分发这些构建产物时，请随附上面的 MIT 许可全文。

`pinyin.json` is compiled into the Go binary through `go:embed` (`shared/search/data.go`) and bundled into `web/dist` (`web/src/lib/search.ts`). Include the MIT license text above when you distribute those build outputs.

## 3. 集成图标 / Integration logos

`web/src/v3/assets/brands/` 下的 46 个 SVG 图标取自下列开源图标集，只用来在集成中心、模型设置、通知路由等处标识对应的产品或服务。商标说明见第 5 节。

The 46 SVG logos in `web/src/v3/assets/brands/` come from the open-source icon sets listed below and only identify the corresponding products and services in the integration hub, model settings, notification routing and similar places. See section 5 for trademarks.

| 来源 | 版本 | 许可 | 文件数 |
| --- | --- | --- | --- |
| [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 Iconify 转换的 [`@iconify-json/logos`](https://www.npmjs.com/package/@iconify-json/logos) | 1.2.14 | CC0-1.0 | 33 |
| [Simple Icons](https://github.com/simple-icons/simple-icons) | 16.32.0（提交 `3173436c`） | CC0-1.0；其中 Harbor、Keycloak 两个图标，Simple Icons 标注按 [Linux Foundation 商标使用规范](https://www.linuxfoundation.org/legal/trademark-usage) | 6 |
| [LobeHub Icons](https://github.com/lobehub/lobe-icons)（`@lobehub/icons-static-svg`） | 1.95.1（提交 `49a2130d`） | MIT，Copyright (c) 2023 LobeHub，全文：[`web/src/v3/assets/brands/LICENSE.lobe-icons`](web/src/v3/assets/brands/LICENSE.lobe-icons) | 6 |
| [Remix Icon](https://github.com/Remix-Design/RemixIcon)，经 Iconify 的 [`@iconify-json/ri`](https://www.npmjs.com/package/@iconify-json/ri) | Remix Icon 4.8.0（`@iconify-json/ri` 1.2.10） | Apache-2.0，全文见仓库根目录 [`LICENSE`](LICENSE)。Remix Icon 从 4.9.0 起改用自定义许可；这里使用的是 4.8.0。 | 1 |

逐个文件：

| 文件 | 来源项目与版本 | 上游图标 | 许可 | 改动 |
| --- | --- | --- | --- | --- |
| `web/src/v3/assets/brands/argo.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `argo-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/confluence.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `confluence` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/docker.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `docker-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/elasticsearch.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `elasticsearch` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/envoy.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `envoy` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/git.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `git-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/github.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `github-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/githubactions.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `github-actions` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/gitlab.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `gitlab-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/grafana.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `grafana` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/helm.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `helm` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/jenkins.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `jenkins` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/kafka.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `kafka-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/kibana.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `kibana` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/kong.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `kong-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/kubernetes.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `kubernetes` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/launchdarkly.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `launchdarkly-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/mysql.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `mysql-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/nginx.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `nginx` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/notion.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `notion-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/okta.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `okta-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/openai.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `openai-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/opentelemetry.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `opentelemetry-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/pagerduty.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `pagerduty-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/postgresql.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `postgresql` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/prometheus.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `prometheus` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/redis.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `redis` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/sentry.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `sentry-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/slack.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `slack-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/teams.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `microsoft-teams` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/telegram.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `telegram` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/terraform.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `terraform-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/vault.svg` | [gilbarbara/logos](https://github.com/gilbarbara/logos)，经 `@iconify-json/logos@1.2.14` | `vault-icon` | CC0-1.0 | 只换封装：Iconify JSON 包成独立 `<svg>`，图形未改 |
| `web/src/v3/assets/brands/harbor.svg` | [Simple Icons](https://github.com/simple-icons/simple-icons) `simple-icons@16.32.0`（提交 `3173436c`） | `harbor` | Simple Icons 标为 custom：[LF 商标使用规范](https://www.linuxfoundation.org/legal/trademark-usage) | 加 `fill="#60B932"`（Simple Icons 数据里的品牌色），路径未改 |
| `web/src/v3/assets/brands/istio.svg` | [Simple Icons](https://github.com/simple-icons/simple-icons) `simple-icons@16.32.0`（提交 `3173436c`） | `istio` | CC0-1.0 | 加 `fill="#466BB0"`（Simple Icons 数据里的品牌色），路径未改 |
| `web/src/v3/assets/brands/jaeger.svg` | [Simple Icons](https://github.com/simple-icons/simple-icons) `simple-icons@16.32.0`（提交 `3173436c`） | `jaeger` | CC0-1.0 | 加 `fill="#66CFE3"`（Simple Icons 数据里的品牌色），路径未改 |
| `web/src/v3/assets/brands/keycloak.svg` | [Simple Icons](https://github.com/simple-icons/simple-icons) `simple-icons@16.32.0`（提交 `3173436c`） | `keycloak` | Simple Icons 标为 custom：[LF 商标使用规范](https://www.linuxfoundation.org/legal/trademark-usage) | 加 `fill="#4D4D4D"`（Simple Icons 数据里的品牌色），路径未改 |
| `web/src/v3/assets/brands/ollama.svg` | [Simple Icons](https://github.com/simple-icons/simple-icons) `simple-icons@16.32.0`（提交 `3173436c`） | `ollama` | CC0-1.0 | 加 `fill="#000000"`（Simple Icons 数据里的品牌色），路径未改 |
| `web/src/v3/assets/brands/victoriametrics.svg` | [Simple Icons](https://github.com/simple-icons/simple-icons) `simple-icons@16.32.0`（提交 `3173436c`） | `victoriametrics` | CC0-1.0 | 加 `fill="#621773"`（Simple Icons 数据里的品牌色），路径未改 |
| `web/src/v3/assets/brands/claude.svg` | [LobeHub Icons](https://github.com/lobehub/lobe-icons) `@lobehub/icons-static-svg@1.95.1`（提交 `49a2130d`） | `claude-color` | MIT，Copyright (c) 2023 LobeHub | 无（与上游文件逐字节相同） |
| `web/src/v3/assets/brands/deepseek.svg` | [LobeHub Icons](https://github.com/lobehub/lobe-icons) `@lobehub/icons-static-svg@1.95.1`（提交 `49a2130d`） | `deepseek-color` | MIT，Copyright (c) 2023 LobeHub | 无（与上游文件逐字节相同） |
| `web/src/v3/assets/brands/langfuse.svg` | [LobeHub Icons](https://github.com/lobehub/lobe-icons) `@lobehub/icons-static-svg@1.95.1`（提交 `49a2130d`） | `langfuse-color` | MIT，Copyright (c) 2023 LobeHub | 无（与上游文件逐字节相同） |
| `web/src/v3/assets/brands/langsmith.svg` | [LobeHub Icons](https://github.com/lobehub/lobe-icons) `@lobehub/icons-static-svg@1.95.1`（提交 `49a2130d`） | `langsmith-color` | MIT，Copyright (c) 2023 LobeHub | 无（与上游文件逐字节相同） |
| `web/src/v3/assets/brands/mcp.svg` | [LobeHub Icons](https://github.com/lobehub/lobe-icons) `@lobehub/icons-static-svg@1.95.1`（提交 `49a2130d`） | `mcp` | MIT，Copyright (c) 2023 LobeHub | `currentColor` 改为 `#000000` |
| `web/src/v3/assets/brands/qwen.svg` | [LobeHub Icons](https://github.com/lobehub/lobe-icons) `@lobehub/icons-static-svg@1.95.1`（提交 `49a2130d`） | `qwen-color` | MIT，Copyright (c) 2023 LobeHub | 无（与上游文件逐字节相同） |
| `web/src/v3/assets/brands/dingtalk.svg` | [Remix Icon](https://github.com/Remix-Design/RemixIcon) 4.8.0，经 `@iconify-json/ri@1.2.10` | `dingding-fill` | Apache-2.0（4.8.0 版） | 包成独立 `<svg>`，`currentColor` 改为 `#0089FF`；文件开头有改动说明注释 |

“图形未改”指 Iconify 数据里的图形原样保留；Iconify 转换 gilbarbara/logos 时用 SVGO 重新编码了路径，渲染结果与原图相同。

"Shape unchanged" means the Iconify shape data is kept as is; Iconify re-encoded the gilbarbara/logos paths with SVGO when converting them, and they render the same as the originals.

## 4. 界面图标 / Interface icons（Lucide）

`web/src/v3/ui/iconPaths.ts` 里的 76 个线性图标的路径取自 [Lucide](https://github.com/lucide-icons/lucide)，按 ISC License 使用（Lucide 中部分图标沿自 Feather，MIT）。版权声明与许可全文：[`web/src/v3/ui/LICENSE.lucide`](web/src/v3/ui/LICENSE.lucide)。

The 76 stroke icons in `web/src/v3/ui/iconPaths.ts` use paths from [Lucide](https://github.com/lucide-icons/lucide) under the ISC License (portions of Lucide come from Feather, MIT). Copyright and license text: [`web/src/v3/ui/LICENSE.lucide`](web/src/v3/ui/LICENSE.lucide).

## 5. 商标 / Trademarks

本项目界面和文档中出现的第三方产品名称、标志和图标（包括 `web/src/v3/assets/brands/` 下的文件）是其各自所有者的商标或注册商标。本项目只用它们来标识 Redstring 可以连接或展示的对应产品与服务，不表示这些所有者认可、赞助本项目，也不表示与本项目有任何关联。

上面各节列出的许可（CC0-1.0、MIT、Apache-2.0）只针对图标文件本身的著作权，不授予任何商标权。fork 或再分发本项目时，请继续只用这些标志指代对应的产品，不要把它们用作你自己的产品标志，不要修改图形让它看起来像新的品牌，并遵守各品牌方的品牌使用规范。品牌所有者如果希望调整或移除自己的标志，请在本仓库提交 issue。

Product names, logos and icons of third parties that appear in this project's interface and documentation (including the files in `web/src/v3/assets/brands/`) are trademarks or registered trademarks of their respective owners. They are used only to identify the products and services that Redstring can connect to or display. Their use does not imply endorsement or sponsorship by, or any affiliation with, those owners.

The licenses listed above (CC0-1.0, MIT, Apache-2.0) cover only the copyright in the icon files and grant no trademark rights. If you fork or redistribute this project, keep using these marks only to refer to the corresponding products, do not use them as your own product's logo, do not alter them to suggest a new brand, and follow each owner's brand guidelines. Brand owners who want their mark changed or removed can open an issue in this repository.

## 6. 通过包管理器获取的依赖 / Package-manager dependencies

`go.mod` 与 `web/package.json`（锁文件 `web/pnpm-lock.yaml`）声明的依赖不随仓库分发，构建时下载，各自的许可见其项目。如果你分发构建产物（Go 二进制、`web/dist`、容器镜像），产物里包含这些依赖，需要按它们的许可附上相应声明。

Dependencies declared in `go.mod` and `web/package.json` (locked by `web/pnpm-lock.yaml`) are not vendored; they are downloaded at build time under their own licenses. If you distribute build outputs (the Go binary, `web/dist` or a container image), those outputs include these dependencies and need their notices.

构建产物同样包含第 2～4 节的内容：拼音词典编进 Go 二进制和前端产物，图标被打进 `web/dist`（小文件可能内联成 data URL）。分发构建产物、演示站或容器镜像时，请一并附上本文件以及 `shared/search/LICENSE.go-pinyin`、`web/src/v3/assets/brands/LICENSE.lobe-icons`、`web/src/v3/ui/LICENSE.lucide` 和仓库根目录的 `LICENSE`。

Build outputs also contain the content of sections 2–4: the pinyin dictionary is compiled into the Go binary and the web bundle, and the icons are bundled into `web/dist` (small files may be inlined as data URLs). When you distribute build outputs, a demo site or a container image, include this file together with `shared/search/LICENSE.go-pinyin`, `web/src/v3/assets/brands/LICENSE.lobe-icons`, `web/src/v3/ui/LICENSE.lucide` and the root `LICENSE`.
