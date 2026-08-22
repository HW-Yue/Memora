# 候选预测器只给路径：检索返回值的收窄

状态：**迁移设计**（2026-08-22）。落实[查询形态](../product/query-model.md) §6
与[架构原则](../product/architecture-principles.md) §3，不是独立规范——
与上位规范冲突时以上位为准。**尚未排期，未开始实现。**

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

| 阶段 | 内容 | 独立可验证的性质 |
|---|---|---|
| 1 | `routelexical.Match` 带上 Path（只加不减） | 路径与 `SHOW ROUTES` 对同一节点给出的路径逐字一致 |
| 2 | 两个语句的返回值加上路径，旧字段仍在 | 新增列正确；既有调用方不受影响 |
| 3 | 定 wire 兼容口径，删掉分数/解释/回执/预算 | 返回值中不再出现第 3 节标「去掉」的任何字段 |
| 4 | `row` kind 的路径接反向索引树 | 依赖 leaf-rowid 迁移的阶段 2 完成 |

**跨阶段基线**：切换前后对同一组查询比对返回的路径集合，逐字一致。

## 关联

- [查询形态](../product/query-model.md) §6（上位）、
  [架构原则](../product/architecture-principles.md) §3
- [叶子直挂 RowID](../storage/leaf-rowid-v1.md)（`row` 的反查在那边定）
- [ADR-0007：Router 权威，候选预测器可组合](../decisions/0007-route-predictor-arsenal.md)
- [ADR-0008：全内容倒排索引](../decisions/0008-full-content-inverted-index.md)
- [架构审计](../development/architecture-audit-2026-08.md)
