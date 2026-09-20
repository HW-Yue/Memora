# 检索四条路与 jev 逐层选择

状态：**方向性结论**（2026-09-20，同日按四条路修订）。基于 SQLite + sqlite-vec。
召回 MSQL 门已删，待 Q2 重写；第 4 节 vec0 隔离是 Q0，未修之前不能重开召回面。

## 一句话

查询一共四条路。内核现役只有语义索引（`SHOW ROUTES`）。关键词与向量是双路召回，
内核表还在，MSQL 门后面重写。jev 是 Skill 层可选选择器，走同一套逐层面，不进内核。

## 1. 四条路（Skill 编排，不是内核概念）

| 路 | 谁走 | 内核 |
| --- | --- | --- |
| 语义索引 | Agent 主路：自己读每层 child 选一个 | `SHOW ROUTES`（现役） |
| 关键词召回 | 一次拿到命中位置 | `mem_postings`（门待重写） |
| 向量召回 | 一次拿到命中位置 | `mem_vectors`（门待重写） |
| jev | 把一层 child 当 `Choice` 交给 jev | 仍是 `SHOW ROUTES`，零内核改动 |

内核**不表达**「有几条路」「选哪条」「全走」。四条路最终都是语义路径，
事实一律 `SELECT` 回表。Skill 可单走或组合。

已删、不要再用：`SHOW ROUTE CANDIDATES`、`SHOW LEXICAL LOCATIONS`、内存 `routelexical`。

## 2. 返回值形态：每段带 ID 的路径

召回面（重写后）每条命中至少是：

- **完整语义树路径，逐段带 `route_id`**——`[{name, route_id}, ...]`，不是单个字符串；
- `database_id` / `table_id`；
- **末端 kind**：`leaf` 或 `branch`；
- `object_id`：仅当命中对象是 Row 时给出（即 RowID）。

分数、理由、命中字段、预测器回执一律不返回，沿用
[候选预测器只给路径](./predictor-path-only-v1.md)。

**喂给 jev 的 option 集应剥掉 ID**，只留 name 与 purpose。ID 不是授权凭据。

### 类型决定后续动作

- **`leaf` + `object_id`**：可直接 `SELECT ... WHERE row_id = :id`；
- **`leaf` 无 `object_id`**：先 `OPEN ROUTE` 再回表；
- **`branch`**：不能回表，只能从该节点继续逐层导航。

## 3. 终止与裁决（Skill 侧）

模型判断当前叶子是否满足需求；不满足则退回 `branch` 往下走。
每个 `leaf` 候选都带一次回表，所以召回 `LIMIT` 按回表成本取值。

## 4. Row 向量与 Q0

**裁定**见 [ADR-0012](../decisions/0012-row-vector-leaf-path.md)：允许 Row 语义向量，
命中直接给叶子路径。四条路模型取代「融合是主路径」那条后果。
[F224](../planning/f224-mandatory-row-route.md) 仍是硬前置。

`mem_vectors` 混装 route 与 row，kind 过滤在 KNN 之后。Row 远多于 Route 时
route 命中会被挤空。这是 Q0，必须先修。

## 5. 两条召回通道的可见性不同

词法 postings 在写事务内同步；向量 embedding 在提交后异步。
开工前必须裁定可见性语义（见执行计划 Q2）。本轮不恢复内核 RRF 融合门。

## 6. 逐层调用

`memora query` 已够用。**逐层 jev 不需要任何新内核能力。**

## 7. jev

见 [jev 作为逐层分支选择器](./jev-branch-selection.md)。写入路径本轮不动，
[F223 fan-out 硬上限](../planning/f223-route-branch-fanout-limit.md)原样保留。

## 关联

- [查询形态](../product/query-model.md)
- [候选预测器只给路径](./predictor-path-only-v1.md)
- [ADR-0012：Row 向量直给叶子路径](../decisions/0012-row-vector-leaf-path.md)
- [执行计划](../planning/execution-plan.md)
