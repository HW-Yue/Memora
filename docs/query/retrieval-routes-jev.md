# 检索四条路与 jev 逐层选择

状态：**方向性结论**（2026-09-20）。产品形态如下。
现在代码已实现语义索引（`SHOW ROUTES`）、关键词召回（`RECALL`）与 SQL 回表；
向量召回在 M7 后续 Feature。

## 一句话

查询一共四条路。内核现役是语义索引与关键词召回。向量召回待实现（M7）。
jev 是 Skill 层可选选择器，走同一套逐层面，不进内核。

## 1. 四条路（Skill 编排，不是内核概念）

| 路 | 谁走 | 现在代码 |
| --- | --- | --- |
| 语义索引 | Agent 主路：自己读每层 child 选一个 | `SHOW ROUTES` |
| 关键词召回 | 一次拿到命中位置 | `RECALL`（FTS5 trigram） |
| 向量召回 | 一次拿到命中位置 | 待实现（M7） |
| jev | 把一层 child 当 `Choice` 交给 jev | 仍是 `SHOW ROUTES`，零内核改动 |

内核**不表达**「有几条路」「选哪条」「全走」。四条路最终都是语义路径，
事实一律 `SELECT` 回表。Skill 可单走或组合。

## 2. 返回值形态：每段带 ID 的路径

召回每条命中至少是：

- **完整语义树路径，逐段带 `route_id`**——`[{name, route_id}, ...]`，不是单个字符串；
- `database_id` / `table_id`；
- **末端 kind**：`leaf` 或 `branch`；
- `object_id`：仅当命中对象是 Row 时给出（即 RowID）。

分数、理由、命中字段、预测器回执一律不返回，见
[查询形态](../product/query-model.md) §6。

**喂给 jev 的 option 集应剥掉 ID**，只留 name 与 purpose。ID 不是授权凭据。

### 类型决定后续动作

- **`leaf` + `object_id`**：可直接 `SELECT ... WHERE row_id = :id`；
- **`leaf` 无 `object_id`**：先 `OPEN ROUTE` 再回表；
- **`branch`**：不能回表，只能从该节点继续逐层导航。

## 3. 终止与裁决（Skill 侧）

模型判断当前叶子是否满足需求；不满足则退回 `branch` 往下走。
每个 `leaf` 候选都带一次回表，所以召回 `LIMIT` 按回表成本取值。

## 4. Row 向量

**裁定**见 [ADR-0012](../decisions/0012-row-vector-leaf-path.md)：允许 Row 语义向量，
命中直接给叶子路径。当前没有向量实现。
[行必须可导航](../planning/row-navigable.md) 仍是产品硬前置。

## 5. 可见性

词法同步 vs 向量异步的可见性，随召回实现一起裁定。

## 6. 逐层调用

`memora query` 已够用。**逐层 jev 不需要任何新内核能力。**

## 7. jev

见 [jev 作为逐层分支选择器](./jev-branch-selection.md)。写入路径本轮不动。
