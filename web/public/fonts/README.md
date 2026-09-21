# AIOps Canvas 字体说明 / Font attribution

AIOps Canvas 使用 **MiSans** 字体。

Copyright © 2020-2025 Beijing Xiaomi Mobile Software Co.,Ltd. All Rights Reserved.

MiSans 按小米的《MiSans 字体知识产权许可协议》使用；字体不受本项目其他源码许可证覆盖。完整官方协议随字体保存在 [MiSans-LICENSE.pdf](./MiSans-LICENSE.pdf)。字体作为本应用的一部分提供，不作为独立字体产品再分发或销售。

## 来源与许可

- 官方主页：https://hyperos.mi.com/font/zh/
- 官方下载页：https://hyperos.mi.com/font/zh/download/
- 官方 FAQ：https://hyperos.mi.com/font/zh/faq/
- 原始字体包：https://hyperos.mi.com/font-download/MiSans.zip
- 包内原始文件：`MiSans/可变字体/MiSansVF.ttf`
- 官方协议：https://hyperos.mi.com/font-download/MiSans字体知识产权许可协议.pdf
- 获取日期：2026-09-21；下载包 HTTP `Last-Modified`：2025-07-14 12:06:28 GMT。

官方 FAQ 允许在任意平台、商业项目中使用字体，允许嵌入式使用，要求在软件中注明 MiSans。协议允许分发使用字体创作的应用，要求保留版权声明及协议，同时限制字体的单独再分发、再许可、售卖和改编。此应用保留原字体版权、完整协议及可见署名。

## 本地文件与完整性

| 文件 | 字节数 | SHA-256 |
| --- | ---: | --- |
| 原始 `MiSansVF.ttf`（不随项目保存） | 20,093,424 | `0ddef90648998900175cfdca9a6f087a2544c182f130b0ad4f7e94a03a115e79` |
| `web/src/assets/fonts/MiSansVF.woff2` | 11,931,324 | `7b831d1f065848b2bf5d24c6cbcb21b72480c8cfd55ea3461a334f90c87a777b` |
| `MiSans-LICENSE.pdf` | 79,535 | `4a93a27cd2bd81b3b5ecfd0a853144a876fa26938a93a68443c67d74172fcb86` |

官方下载 URL 未固定版本，因此以上原始文件和交付文件的哈希用于锁定本次实际字节。取出 ZIP 成员时已核对其原始大小和 CRC-32。

`MiSansVF.woff2` 由官方原始 TTF 使用 fontTools 4.65.0 的 WOFF2 容器压缩生成。没有裁剪字符、限制字重轴、改名字、改字形或调整间距。完整字体的 29,773 个字形、29,571 个 Unicode 映射及变体保留；未使用仅根据当前页面文本生成的子集。此覆盖范围属于 MiSans 主字体，不代表额外 MiSans L3 生僻字库或其他语言家族也已打包。字体仍未覆盖的字符由系统后备字体呈现。

字体内置名称：`MiSans VF`；PostScript 名称：`MiSansVF`；版本：`4.009`；原生 `wght` 轴范围 `150–700`（默认 `330`）。产品使用正常和较厚字重，避免默认较细字重。

## 使用

字体随应用自托管，运行时不依赖小米 CDN、Google Fonts 或用户本机安装。字体文件在 `web/src/assets/fonts/`，构建时由 Vite 输出到带内容哈希的 `/assets/` URL，复用现有一年不可变缓存；页面通过同一构建资源预加载。许可和声明在 `/fonts/` 保留。`web/src/typography.css` 中定义项目别名：

```css
@font-face {
  font-family: 'MiSans';
  src: url('./assets/fonts/MiSansVF.woff2') format('woff2');
  font-style: normal;
  font-weight: 150 700;
  font-display: swap;
}
```

完整中文可变字体约 11.38 MiB；浏览器初次下载期间使用后备字体，加载完成后中英文字体统一。此文件同时提供多个字重，避免分别下载多份完整中文字库。发布时应为字体配置合理缓存；更新文件后应更新资源版本或 URL，避免旧缓存。

## 重新生成

使用上述官方原始字体文件和临时安装的 `fonttools[woff]==4.65.0`，不需要把字体工具添加为应用依赖：

```python
from fontTools.ttLib import TTFont

font = TTFont('MiSansVF.ttf')
font.flavor = 'woff2'
font.save('MiSansVF.woff2')
```

再次生成的压缩文件哈希可能因时间戳或压缩库版本不同而改变；更新时应重新记录哈希，并核对所有字符映射、字形坐标、可变轴、变体、OpenType 排版与版权信息均被保留。
