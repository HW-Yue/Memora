# F227：Admin UI 的归档规则

状态：**已实现（2026-08-20）**。[F227 统一归档模型](./f227-object-archive.md) 的前端部分。

**实现时的一处重要修正**：本文档初稿假定前端可以执行归档／取消归档。实际不行——
Admin Gateway 是硬性只读的（`internal/msql/readquery/policy.go` 的 `Allowed`
只放行 `SHOW`／`DESCRIBE`／`SELECT`／`OPEN ROUTE`／两种 `PLAN`），`ARCHIVE` 不在
其中，也**不应该**为此放宽——给一个只读网关开写口子，收益远小于代价。
因此前端不做归档按钮，改为在归档对象页面上**原样显示可执行的 `UNARCHIVE` 语句**，
由用户在 CLI 里执行。下面「归档与取消归档的操作面」一节按此重写。

**规则一句话：除非用户主动点开归档，否则任何归档对象在前端不可见。**

## 引擎侧已经保证了"默认不可见"

需要先讲清楚责任划分：**前端不可能因为漏了过滤而泄漏归档对象**。
引擎侧 `nativecatalog` 的 `DescribeDatabase`／`DescribeTable`／`ShowDatabases`／
`ShowTables` 直接拒绝或过滤归档对象，只有显式带 `INCLUDING ARCHIVED` 的语句
才拿得到。所以前端要做的不是"过滤"，而是"什么时候去要"。

## 默认过滤是全局的，不是每页各做一次

现有 6 个视图（`app.js:1-6`）——`catalog`、`routes`、`rows`、`changes`、
`diffs`、`traces`——各自直接拼 MSQL。`SHOW DATABASES … COMPACT` 就分别写在
`catalog.js:294`、`changes.js:327`、`traces.js:422` 三处。

**不要在这 6 个文件里各加一次过滤。** 六处手写等于六个漏点，
而不变量要求"漏一处就是 bug"。

做法：`catalog.js` 里只有**一个** `archived(archiveMode)` 辅助函数拼出这个修饰词，
所有 loader 调它。bundle 测试断言 `" INCLUDING ARCHIVED"` 这个字面量在整个模块里
**恰好出现一次**——防止将来新增 loader 时手写字符串而漏掉模式。

## 归档模式是一个显式的全局状态

- 入口放在全站导航里（与 6 个视图同级），不是每页角落的复选框；
- 打开后所有视图同时切到 `INCLUDING ARCHIVED`，并且**整站有持续可见的标识**
  ——顶部条带或明显的 `route-archived` body class 改变配色。
  用户必须一眼知道自己正在看归档；
- 归档行在列表里必须自带标记，显示 `archived_at` 与 `archived_reason`，
  不能只靠全局标识区分；
- 该模式**不写进 URL 之外的任何持久存储**。刷新回到默认的"排除归档"。
  一个会被记住的"显示已删除的东西"开关，迟早让用户在错误的视图下做决定。

## 深链接必须说实话

`/rows/<id>`、`/routes/<id>`、`/catalog/<db>` 都是可直接访问的深链接
（`app.js:94-131`）。指向归档对象时：

- **不返回 404**，也**不照常渲染**；
- 渲染对象内容，同时置顶不可忽略的"已归档"横幅，写明归档时间、原因、
  以及是哪一级祖先导致它不可见（对象自身 / 所属 Table / 所属 Database）；
- 页面内所有写操作入口置灰，提示先 `UNARCHIVE`。

祖先归因这一条不能省：用户看到一个 Row 显示"已归档"，
最常见的下一个问题就是"我没归档它啊"——答案是它的 Table 被归档了。

## 归档与取消归档的操作面

Admin Gateway 只读，前端不执行任何归档操作。归档对象的页面顶部给出
`archive-note`：归档时间、原因，以及一条可复制的 `UNARCHIVE DATABASE x` /
`UNARCHIVE TABLE x.y`。用户在 CLI 里执行。

这不是妥协，是正确的边界：归档是 L2 结构性操作，需要 `REASON` 与授权信封，
这些在只读的浏览器会话里本来就凑不齐。

## 不做数量限制

已归档列表与活跃列表一样，**前端不设上限**，走 cursor 一直取到
`page.truncated` 为 false。这与 F223 定下的"限制是给 AI 的、不是给 UI 的"一致，
也与 `routes.js` 已经改成的 `drainPages()` 一致。

## 实现说明（2026-08-20）

- 开关是侧栏里与三个视图同级的 `data-archive-toggle` 按钮，`aria-pressed` 表达状态；
- 全站标识：`body.archive-mode` 加顶部色条，外加 header 上的 `archive-badge`；
- 不持久化：`archiveMode` 是 `app.js` 里的模块变量，刷新即回到默认。
  bundle 测试禁掉 `localStorage`／`sessionStorage`／`document.cookie` 三条持久化路径；
- 深链接：归档对象照常返回内容并渲染 `archive-note`，不 404、不当作活跃对象；
- 已归档列表与活跃列表共用同一套 cursor 分页，不设页数上限。

## 完成门

- 默认模式下归档对象不出现在任何读面——这一条由引擎侧的
  [可见性矩阵测试](../../internal/daemon/f227_visibility_matrix_test.go) 保证，
  它逐个读面断言，比前端逐视图断言更强；
- 打开归档模式后全站同时生效，且整站标识可见；
- 刷新后回到默认模式；
- 归档对象的深链接返回 200、显示横幅、写明归因祖先、写操作入口置灰；
- 归档对象页面给出可复制的 `UNARCHIVE` 语句（Gateway 只读，不代为执行）；
- 已归档列表跟随 cursor 取尽，不设页数上限；
- `internal/adminui/bundle.go` 的冻结 SHA256/大小随资产更新。

## 关联

- [F227 统一归档模型](./f227-object-archive.md)
- [F223 Route Branch Fan-out 上限](./f223-route-branch-fanout-limit.md)
