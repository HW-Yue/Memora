# 语义 Router

状态：已实现。叶子直接挂 RowID，见 [写入形态](../product/write-model.md)。

Router 是给 Agent 读的多层语义目录。每个 Table 一棵树。它不是 B+ Tree、向量索引或答案来源。

```text
Database
└── Table
    ├── 产品边界          (branch)
    │   ├── AI 与引擎     (leaf → row_id)
    │   └── 永久非目标    (leaf → row_id)
    └── 查询流程          (branch)
```

叶子最多挂一个活跃 Row，一行也只占一个叶子（1:1，2026-09-21 修订），正文只存一份。
`OPEN ROUTE` 的结果是 0 或 1 条 locator，不是候选桶。

## 导航

```sql
SHOW ROUTES FROM TABLE work.notes AT ROOT;
SHOW ROUTES UNDER :route_id LIMIT 12;
OPEN ROUTE :leaf_id LIMIT 1;
SELECT * FROM work.notes WHERE row_id = :row_id LIMIT 1;
```

每次只返回一层。路径、子节点、预算构成 Route Frame，用完即丢，不进长期 system prompt。
默认 `SHOW ROUTES` 只带 purpose；分不清时再 `DESCRIBE ROUTE`。
Router 与 `OPEN ROUTE` 只给位置，不给正文。

字段与分页见 [Route Read](./route-read-v1.md)。

## 结构上限

一个 root 或 branch 最多 `route_policy.branch_fanout` 个 live child，默认 12。
第 N+1 个失败，出路是重构子树或提高本库上限。见 [写入形态](../product/write-model.md) §4.3。
已占用叶子不接收第二行，Agent 必须新建叶子。

## 写入时的树

INSERT / SPLIT 必须让每个 live 行挂在**恰好一个**叶子上，已在提交路径强制
（[行必须可导航](../planning/row-navigable.md)）。
UPDATE 缺省 `route_leaf_id` 表示保留挂载；DELETE 改为[归档后物理删除](../product/row-delete-archive.md)，
行与它那个叶子一起没了。
改树用 `CREATE ROUTE` / `PLAN ROUTE MUTATION`，带 expected revision。引擎不替 Agent 起名字。
