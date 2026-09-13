# 数据行之间的双向链接

状态：**方向性结论**（2026-09-13），「待确认」一节除外。
分层前提见 [ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)；
链接遇到废弃行的处理见 [数据行的生命周期](./row-lifecycle-successor.md)。

## 一句话

链接是数据行上的一个字段：写明链到哪一行，并带上那一行的摘要。
不单独建关系表，也不是独立的对象。

## 字段

数据表的每一行带一个默认字段 `links`（与 `route_leaf_ids` 同类，先存为 TEXT 里的 JSON）：

```text
links: [
  { row_id: 对方行的 RowID, summary: 对方行的摘要 },
  ...
]
```

RowID 自带表空间，全局唯一，所以链接可以跨表，不必再写 table。

## 双向

A 链到 B 时，**一个事务内**同时写两面：

- A 的 `links` 加 `{ B, B 的摘要 }`；
- B 的 `links` 加 `{ A, A 的摘要 }`。

删除链接同样两面一起删。任何时候两面必须一致，不允许只有一面。

读一行时，`links` 里的摘要让 Agent 不回表就知道对面是什么；
要事实仍按 `row_id` 点查对方行。

## 与废弃行

对方行被拆分或合并后，链接按[生命周期](./row-lifecycle-successor.md)懒更新：
改指全部接替者，两面一起改，每个接替者的摘要取自接替者本身。

## 取代现役 Relation

现役 Relation 是 objects 树里的独立对象（`relation/model.go`：source、target、
relation_type、description、revision）。本文取代它：链接进数据行字段，
objects 树里的 Relation 族随 ADR-0011 第 4 步退役。

## 待确认

- **链接类型**：现役 Relation 有 `relation_type`（如「依赖」「引用」）和 `description`，
  新形态是否保留类型字段；
- **摘要过期**：对方行原地修改后，这边存的摘要是否跟着更新、何时更新
  （写时同步会让一次修改牵动所有链接方；懒更新需要记下对方的 revision 来判断过期）；
- **history 噪声**：链接字段在数据行上，加一条链接会给两行各记一条 history，
  摘要刷新是否也记；
- **数量上限**：一行最多挂多少条链接（单行正文有 8 KiB 上限）。
