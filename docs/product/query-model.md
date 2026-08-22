# Memora 查询形态

状态：**最高产品参考规范**（2026-08-22）。本文是查询路线的权威产品规范，与
[`write-model.md`](./write-model.md) 配套：写入形态定义数据如何落库，本规范定义数据
如何被找到。与本规范冲突时，以本文为准。

## 1. 核心原则

1. **查询是一条有界的导航链路，不是全库扫描**。从一个问题出发，逐层缩小范围，
   每步拿到稳定 ID 作为下一步的输入，最终定位到唯一一条 Row。
2. **中间结果不是答案**。语义导航返回的 route / path / purpose 只是"该往哪走"的
   指引，不是数据本身；答案来自最后对业务表的点查。
3. **每一步有界**：返回有界条数、稳定 ID、快照与结构化错误，AI 不需要把全库目录
   装进上下文。
4. **查询与写入共用同一棵语义索引树**：写入时把 RowID 挂到叶子，查询时从叶子拿
   RowID——读写走同一条导航路径。

## 2. 查询路线（从问题到答案）

```text
问题/意图
→ ① 发现：定位库和表
→ ② 语义导航：逐层走到叶子
→ ③ 定位行：叶子拿 RowID
→ ④ 回表取值：业务表点查
→（可选）⑤ 查历史：history_id → history 表
```

## 3. 每一步：找到什么、拿它找下一个

### ① 发现：定位库和表

- **入口**：`SHOW CATALOG ATLAS`——一次返回授权范围内全部 Database 与 Table 的短语义
  描述（`kind`、`database_id`、`database`、`table_id`、`table`、`purpose`、`scope`）。
- **找到**：一个 Table 的稳定 `database_id` / `table_id` 及其用途。
- **拿它找下一个**：选定 Table → `DESCRIBE TABLE` 确认它的 `row_semantics`、列、
  `schema_version`，确认"这表装的是什么、一行的语义是什么"。

### ② 语义导航：逐层走到叶子

- **入口**：`SHOW ROUTES FROM TABLE ... AT ROOT`——返回第一层节点；每行含
  `route_id`、`path`、`name`、`kind`（branch / leaf）、`purpose`。
- **逐层**：选一个 branch 的 `route_id` → `SHOW ROUTES UNDER :route_id` 拿下一层；
  重复直到 leaf。每次只取一层，不一次拉全树。
- **找到**：一个 **leaf** 的 `route_id` 和它描述的语义。
- **拿它找下一个**：leaf → 直接拿到挂载的 RowID（新写入形态下，**叶子直接挂 RowID**，
  不再依赖独立的 membership 关系）。

### ③ 定位行：叶子拿 RowID

- **入口**：叶子已直接挂 RowID（写入时挂上去的），或 `OPEN ROUTE :leaf` 取出。
- **找到**：唯一的 `row_id`（一个叶子最多一个活跃 Row）。
- **拿它找下一个**：`row_id` → 业务表点查。

### ④ 回表取值：业务表点查

- **入口**：`SELECT ... WHERE row_id = :row_id`。
- **路径**：`row_id` 走业务表 B+ 树索引点查，直接取到那一条数据。
- **找到**：真实数据（业务列）+ `history_id`（指向它的 history 记录）。
- **答案**：从这里出来，引用 `database.table`、`row_id`、`revision` 作为来源。

### ⑤ 查历史（可选）

- **入口**：拿业务表的 `history_id`。
- **路径**：`history_id` → history 表 B+ 树点查。
- **找到**：这条数据从创建到现在的每次变更记录（谁、从哪、为什么、何时）。

## 4. 对应关系一览

| 步骤 | 找到 | 拿它找 | 存储 |
|------|------|--------|------|
| ① 发现 | database_id / table_id + 用途 | 选表 → DESCRIBE | Catalog |
| ② 语义导航 | leaf 的 route_id + 语义描述 | 叶子 → RowID | 语义索引树 |
| ③ 定位行 | row_id | row_id → 业务表 | 语义索引叶子（挂 RowID） |
| ④ 回表取值 | 真实数据 + history_id | 答案 | 业务表（B+ 树） |
| ⑤ 查历史 | 变更记录 | 历史 | history 表（B+ 树） |

## 5. 查询与写入的对应

| | 写入（write-model） | 查询（本规范） |
|---|---|---|
| 起点 | 意图识别：要写什么 | 意图：要查什么 |
| 语义树 | 查重 → 建叶子 → 挂 RowID | 导航叶子 → 拿 RowID |
| 数据 | 写业务表 + history_id | 读业务表 |
| 历史 | 写 history 表 | 读 history 表（history_id） |

读写共用同一棵语义索引树：写入把 RowID 挂到叶子，查询从叶子拿 RowID，同一导航路径，
不维护两套定位。

## 6. 边界

- 禁止全库扫描后把大量内容塞给模型选择，必须逐层缩小导航范围。
- 禁止把 route / path / 候选当作答案或事实来源；答案只能来自业务表点查结果。
- 每一步都有结果与输出预算；找不到时区分"没有匹配 Row"、"截断"、"过期"、
  "权限不足"，不猜、不编。
