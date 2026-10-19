# 第三方内容声明 / Third-party notices

Redstring 自己的源码按 [Apache-2.0](LICENSE) 授权。下面这些随仓库一起分发的第三方内容**不受 Apache-2.0 覆盖**，各自按原许可使用。fork 或再分发本项目时，请一并保留这些声明，并自行确认符合各自的许可条件。

Redstring's own source code is licensed under [Apache-2.0](LICENSE). The third-party content below ships with this repository but is **not** covered by Apache-2.0; each item keeps its own license. Keep these notices when you fork or redistribute the project, and check that your use meets each license.

## 1. MiSans 字体 / MiSans font

| 项 | 内容 |
| --- | --- |
| 文件 | `web/src/assets/fonts/MiSansVF.woff2`（构建时输出到 `/assets/`） |
| 版权 | Copyright © 2020-2025 Beijing Xiaomi Mobile Software Co.,Ltd. All Rights Reserved. |
| 许可 | 《MiSans 字体知识产权许可协议》（MiSans Font Intellectual Property License Agreement），全文：[`web/public/fonts/MiSans-LICENSE.pdf`](web/public/fonts/MiSans-LICENSE.pdf) |
| 来源 | 小米官方字体包 `MiSans/可变字体/MiSansVF.ttf`，<https://hyperos.mi.com/font/zh/> |
| 改动 | 只把 TTF 用 fontTools 4.65.0 换成 WOFF2 容器压缩，没有裁剪字符、改名或改字形；来源、哈希和生成方法见 [`web/public/fonts/README.md`](web/public/fonts/README.md) |

MiSans 不是开源字体。协议要求在软件中注明使用了 MiSans、在字体的任何副本中保留版权声明和协议，不允许改编字体，也不允许把字体或其副本单独出租、再许可、赠与、出借、再分发或出售；用 MiSans 创作的应用等作品可以分发。字体只为本应用的界面使用，**不要把字体文件从本项目中取出单独使用或分发**。如果你的用途不确定是否符合协议，可以把字体换成别的字体：改 `web/src/v3/design/base.css` 与 `web/src/typography.css` 里的 `@font-face` 和字体栈，并去掉 `web/index.html` 里的字体预加载，界面会退回系统字体。

MiSans is not an open-source font. Its license requires the software to state that it uses MiSans and every copy of the font to keep the copyright notice and the agreement; it forbids adapting the font and renting, sublicensing, giving, lending, redistributing or selling the font or its copies on their own, while works created with it (such as applications) may be distributed. The font is here only for this application's interface; **do not extract it for separate use or distribution**. If you are unsure whether your use fits the license, replace it by editing the `@font-face` rules and font stacks in `web/src/v3/design/base.css` and `web/src/typography.css` and removing the font preload in `web/index.html`; the interface then falls back to system fonts.

## 2. 拼音词典 / Pinyin dictionary（go-pinyin）

| 项 | 内容 |
| --- | --- |
| 文件 | `shared/search/pinyin.json`，以及由同一数据生成的 `migrations/019_search_pinyin.sql`；浏览器、Go 内存搜索和 PostgreSQL 检索共用 |
| 来源 | [go-pinyin v0.21.0](https://github.com/mozillazg/go-pinyin/tree/v0.21.0) 的完整字符词典（41,923 字），每字只保留首个无声调读音；生成器为 `scripts/generate-search-dictionary.go` |
| 版权与许可 | Copyright (c) 2016 mozillazg，MIT License，全文：[`shared/search/LICENSE.go-pinyin`](shared/search/LICENSE.go-pinyin) |

## 3. 集成图标 / Integration logos

`web/src/v3/assets/brands/` 下的 46 个 SVG 图标只用来在集成中心、模型设置等处标识对应的产品或服务。这些名称和标志是各自所有者的商标；使用它们不代表这些公司认可、赞助或与本项目有关联。

The 46 SVG logos in `web/src/v3/assets/brands/` only identify the corresponding products and services in the integration hub, model settings and similar places. The names and logos are trademarks of their respective owners; their use does not imply endorsement, sponsorship or affiliation.

仓库里没有记录这些图形文件的来源。按文件格式推断如下，_v0.1.0 发布前核对并补齐来源与许可：_

| 文件 | 推断的来源 | 该来源的许可 |
| --- | --- | --- |
| `claude.svg`、`deepseek.svg`、`langfuse.svg`、`langsmith.svg`、`mcp.svg`、`qwen.svg` | [LobeHub Icons](https://github.com/lobehub/lobe-icons)（文件内带 `lobe-icons` 标识或其导出格式） | MIT（_待核对_） |
| `harbor.svg`、`istio.svg`、`jaeger.svg`、`keycloak.svg`、`ollama.svg`、`victoriametrics.svg` | [Simple Icons](https://github.com/simple-icons/simple-icons)（24×24、`role="img"` 加 `<title>` 的格式） | CC0-1.0（_待核对_） |
| 其余 33 个彩色标志（多为 256 宽，如 `kubernetes.svg`、`prometheus.svg`、`gitlab.svg`） | 可能来自 [gilbarbara/logos](https://github.com/gilbarbara/logos)（SVG Porn） | CC0-1.0（_待核对_） |
| `dingtalk.svg` | _未知，待核对_ | _待核对_ |

## 4. 通过包管理器获取的依赖 / Package-manager dependencies

`go.mod` 与 `web/package.json`（锁文件 `web/pnpm-lock.yaml`）声明的依赖不随仓库分发，构建时下载，各自的许可见其项目。如果你分发构建产物（Go 二进制、`web/dist`、容器镜像），产物里包含这些依赖，需要按它们的许可附上相应声明。

Dependencies declared in `go.mod` and `web/package.json` (locked by `web/pnpm-lock.yaml`) are not vendored; they are downloaded at build time under their own licenses. If you distribute build outputs (the Go binary, `web/dist` or a container image), those outputs include these dependencies and need their notices.
