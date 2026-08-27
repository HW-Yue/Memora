# 候选预测器只给路径：检索返回值的收窄

状态：**迁移设计**（2026-08-22，2026-08-25 更新进度）。落实
[查询形态](../product/query-model.md) §6 与
[架构原则](../product/architecture-principles.md) §3，不是独立规范——
与上位规范冲突时以上位为准。

**四个阶段均已完成。** 阶段 4 的落地形态见 §7「阶段 4 的三处裁定」。

编写原则同[存储层总览](../storage/README.md)：每条「现状」断言都能指到具体文件与行。

## 一句话

关键词检索与向量检索**只回答"命中的东西在语义树的哪个位置"**。
返回完整路径，其余全部去掉——分数、理由、命中字段、预测器回执、预算四元组。

## 1. 现状：返回的全是别的

### `SHOW ROUTE CANDIDATES … USING LEXICAL|VECTOR`

`Rows` 为空，全部内容装在一个特殊的 Discovery Frame 信封里
（`internal/discovery/frame.go`）：

| 结构 | 位置 | 字段 |
|---|---|---|
| `Candidate` | `frame.go:50` | `database_id`／`table_id`／`route_id`／`route_revision`／`predictor`／`reason`／`score_kind`／`score`／`matched_fields` |
| `Frame` | `frame.go:62` | `version`／`usage`／`snapshot`／`catalog_revision`／`budget`／`predictors`／`candidates`／`truncated` |
| `Budget` | `frame.go:34` | `candidate_limit`／`utf8_byte_limit`／`candidates_used`／`utf8_bytes_used` |
| `PredictorReceipt` | `frame.go:41` | `predictor`／`status`／`score_kind`／`reason`／`candidate_count`／`truncated` |

执行入口：`showLexicalRouteCandidates`（`executor/route_candidates.go:63`）、
`showVectorRouteCandidates`（`:127`）。**没有一个字段是路径。**

### `SHOW LEXICAL LOCATIONS …`

返回真行（`executor/lexical_locations.go:84-88`）：
`kind`／`database_id`／`table_id`／`object_id`／`revision`／
`matched_term_count`／`matched_field_count`／`frequency`／`matched_field_ids`。
**也没有路径。**

### 一个反讽

`routelexical` **已经把路径读进来了**：`search.go:281` 往 `routerView` 里放了
`Path: node.Path`，`search.go:289` 甚至把 `route.path` 作为一个可检索字段建了索引。
然后 `search.go:285` 组装 `Match` 时只取
`{DatabaseID, TableID, RouteID, RouteRevision}`——**路径被丢掉了**。
`Match` 结构（`search.go:33-40`）里根本没有 Path 字段。

而 `router.Node.Path` 是**存下来的**（`router/model.go:28`，创建/改名/移动时维护），
拿到节点就有完整路径，不需要回溯父节点。

**这件事的成本几乎为零：数据已经在手里，只是没带出来。**

## 2. 目标：返回什么

`database` + `table` + **完整语义树路径**，仅此而已。

### 五种命中对象各自的路径

`SHOW LEXICAL LOCATIONS` 的 `kind` 有五种
（`lexicallocation/search.go:384-411`），路径定义如下：

| kind | 路径 |
|---|---|
| `route` | 该节点的 `Node.Path`（直接取，已存） |
| `row` | 该 Row 所挂**全部叶子**的 `Node.Path`（一行可属多叶，故是列表） |
| `column` | 所属 Table 的路径 + 列名 |
| `table` | 表级，路径为该 Table 的语义树根 |
| `database` | 库级，无树内路径；只返回 `database` |

`row` 那一行依赖「行 → 叶子」反向查找。这正是
[叶子直挂 RowID](../storage/leaf-rowid-v1.md) 里定的反向索引树要承担的事，
两份设计在这里对接：**本设计不另造反查机制。**

## 3. 前后对比：每个字段的去留

| 字段 | 去留 | 理由 |
|---|---|---|
| **路径** | **新增** | 这是唯一要返回的东西 |
| `database_id`／`table_id` | 保留 | 路径是表内相对的，需要它们定位到哪张表 |
| `route_id` | **去掉** | 有路径就能导航；裸 ID 还要再换算一步 |
| `object_id`（row/column） | 保留 | 是"数据项"本身的身份，不是换算中间物 |
| `revision`／`route_revision` | **去掉** | 预测器是提示，不承担版本契约；版本由回表时的读取给出 |
| `score`／`score_kind` | **去掉** | 分数一旦外露，调用方就会拿它当权威 |
| `reason` | **去掉** | 解释 = 第二个权威 |
| `matched_fields`／`matched_field_ids` | **去掉** | 同上，且会诱导调用方按字段再过滤 |
| `matched_term_count`／`matched_field_count`／`frequency` | **去掉** | 换了名字的分数 |
| `predictor`／`PredictorReceipt` 整体 | **去掉** | 调用方不需要知道是哪个预测器、它是否成功；失败就是没有结果 |
| `Budget` 四元组 | **去掉**，只留 `limit` | 有界靠 `limit` 表达，用不着回报用量 |
| `snapshot`／`catalog_revision` | 保留 | 不是分数，是"这批结果基于哪个视图"的一致性凭据 |
| `truncated`／`cursor`／`next_cursor` | 保留 | 有界与分页是宪章要求 |

去掉之后，`Candidate` 从 9 个字段变成 3-4 个，`Frame` 不再需要
`Budget` 与 `Predictors` 两个子结构。

## 4. 排序：内部保留，外部不暴露

命中数超过 `limit` 时总得决定留哪些。**现有排序逻辑一律保留，只是不进返回值**：

- 关键词：`MatchCount` + `specificity()`（route > table > database），
  `routelexical/search.go:114-166,331-356`；
- 向量：点积 top-K，`routeexact/search.go:60+`；
- 位置聚合：`aggregatePostings` + `lessLocation` + `kindRank`，
  `lexicallocation/search.go:319-462`。

**对外按路径字典序稳定输出。** 不写死这一条，实现时必然各自发挥。

## 5. 改动点

| 文件 | 改动 |
|---|---|
| `internal/discovery/frame.go` | `Candidate` 与 `Frame` 瘦身；删 `Budget`、`PredictorReceipt`、`ScoreKind` |
| `internal/routelexical/search.go` | `Match` 加 Path 字段，把 `search.go:281` 已读到的 `node.Path` 带出来 |
| `internal/msql/executor/route_candidates.go` | 两个执行函数改为组装路径 |
| `internal/msql/executor/lexical_locations.go` | 行组装改为路径；`row` kind 走反向索引树 |
| `internal/result/envelope.go` | **这是已发布的 wire 格式**，见下 |

### wire 兼容

`internal/result/envelope.go` import 了 `discovery`——Discovery Frame 是
**结果信封的一部分，属已发布格式**。瘦身它等于改 wire。迁移必须写明：
是提 envelope 版本号、还是保留旧字段一段时间只是不再填充。
**本设计不替这个决定拍板**，但要求实现前先定，且与
`protocol/msql/protocol.go` 的支持窗口一起定。

## 6. 顺带记两个缺陷（不在本设计范围内解决）

1. **向量检索没有生产发布方。** `routevector.Service.Publish`
   （`service.go:27`）只有测试在调；生产只经 `OpenActive` 读。
   所以 `USING VECTOR` 实际返回 `PredictorUnavailable`。
   已裁定保留向量方向，但"发布方缺失"是缺陷，不是设计；
2. **两处每查询重建全量。** `routelexical.Search` 每次调用重建整个倒排 map
   （`search.go:124`），外加对整个 catalog+route 视图做一次 SHA-256
   （`search.go:175-297`）；`routevector` 的 `Generation.vectors`
   （`model.go:125`）把一个 generation 的全部 route 向量装进内存，
   且 `OpenActive` 每次查询重新加载并重新校验，无缓存。

两条都记入[架构审计](../development/architecture-audit-2026-08.md)与
[已知风险](../development/known-risks.md)。

## 7. 分阶段与验证门

| 阶段 | 内容 | 独立可验证的性质 | 状态 |
|---|---|---|---|
| 1 | `routelexical.Match` 带上 Path | 路径与 Route 节点自己的 `Path` 逐字一致，不是重算的 | **已完成** |
| 2 | 两个语句返回路径 | route 与 table 命中带 path；路径取自 Router | **已完成** |
| 3 | 删掉分数／解释／回执／预算 | 返回值与 wire 上都不再出现第 3 节标「去掉」的字段 | **已完成** |
| 4 | `row`／`column` 的路径接反查 | `row` 带 `paths`（每个叶子一条）；`column` 带 Table 根路径 + 列名 | **已完成** |

### 阶段 4 的三处裁定

**一、`row` 用 `paths`（复数），不用 `path`。** 一行可挂多个叶子，
§2 的表格本来就写着「故是列表」。旁边再放一个标量 `path` 会是同一个问题的
第二个答案。

**二、挂在零个叶子上的 Row 不带这个字段，而不是带一个空列表。**
空列表读起来像「在根上」；没有归属的 Row 在语义树里根本没有位置。
`column` 同理：所属 Table 没有 Route 根就不给路径，而不是拿名字拼一个——
那会是 Router 从没说过的第二种拼法。

**三、读不到那一行就不给路径，不报错。** 这个清单是「往哪儿导航」的提示；
一条在索引读与本次读之间被删掉的 Row 在树里确实没有位置了，
为它让整页查询失败，是把一个过期提示变成一个坏掉的查询。

路径本身仍然只从 Router 节点取。`column` 的最后一段（列名）是这里拼的，
但会漂移的那一段不是。

### wire 兼容口径（阶段 3 定的）

**旧字段删掉，版本号提到 `memora.discovery-frame/v2`**，不保留不填。
一个永远为空的字段是个会被下游当真的谎；提版本号让不兼容变成一次响亮的失败，
而不是一批悄悄读到零值的调用方。

### 阶段 3 落地时改了设计的两处

**一、排序与截断的顺序，原文没写清，第一版我做反了。**
先按路径排序再截断，会让 `/a...` 开头的路径挤掉排名靠前的命中——那是把排序
**扔掉**，不是「内部保留」。正确顺序是：**按排名截断，再按路径排序输出**。
谁能留下由排名决定，怎么列出来由路径决定。

**二、`Budget` 去掉了，但 `BYTES` 子句仍然生效。**
去掉的是**回报用量**（`candidates_used`／`utf8_bytes_used`），不是入参上界。
语句允许调用方要求字节上界，那它就得真的咬人，只是不再回报用掉多少。

### 预测器不可用：从成功回执改为报错

Frame 不再带 `PredictorReceipt`，「向量预测器没有可用 generation」没地方放进一个
成功的回答里了。返回零个候选会宣称「搜过了，树里没有」——是假话。
现在返回 `not_found`。

**跨阶段基线**：切换前后对同一组查询比对返回的路径集合，逐字一致。

## 关联

- [查询形态](../product/query-model.md) §6（上位）、
  [架构原则](../product/architecture-principles.md) §3
- [叶子直挂 RowID](../storage/leaf-rowid-v1.md)（`row` 的反查在那边定）
- [ADR-0007：Router 权威，候选预测器可组合](../decisions/0007-route-predictor-arsenal.md)
- [ADR-0008：全内容倒排索引](../decisions/0008-full-content-inverted-index.md)
- [架构审计](../development/architecture-audit-2026-08.md)
