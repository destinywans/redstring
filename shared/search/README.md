# 统一文本搜索字典

`pinyin.json` 是浏览器、Go 内存搜索和 PostgreSQL 检索共同使用的字典，来源为
[go-pinyin v0.21.0](https://github.com/mozillazg/go-pinyin/tree/v0.21.0)
的完整 41,923 字符词典。保留其每字首个无声调读音；MIT 授权见
`LICENSE.go-pinyin`。它不是手写的常用汉字子集。

从仓库根目录运行 `go run scripts/generate-search-dictionary.go` 同时生成 JSON
和 `migrations/019_search_pinyin.sql`。SQL 函数模板为
`scripts/search-functions.sql`。生成器不连接数据库。

匹配规则：

- 首先按原文不区分大小写进行字面子串匹配；空查询匹配所有候选。
- 不包含 ASCII 字母的查询只使用原文字面匹配，避免不同中文词因首字母相同而误命中。
- 包含 ASCII 字母时，候选生成全拼和汉字首字母两条检索键。英文及数字保持原样，去除空白，`ü` 统一为 `v`；标点包括 `%`、`_` 保留字面意义。
- 混合查询中出现的汉字在候选键中保留原字；查询本身不音译。例如 `资源guanli`、`zi源`、`资源gl`、`Redis缓存`。
- 不做多音词语境推断、音调输入纠错、任意全拼与缩写组合；同一字的读音以此版本词典为准。增加这些能力需同步三端算法及数据，不能单端替换为不同词组词典。

后端 `aiops_search_match(value, query)` 接受普通查询字符串，不要附加 `%` 或做
LIKE 转义。分页、计数、ACL 在各业务查询原有的位置执行；该函数不改变授权。
SQL 按需转写适合当前本机数据规模；大规模数据应另行评估预计算索引。
PromQL、LogQL 等遥测查询语法不经过此匹配器。
