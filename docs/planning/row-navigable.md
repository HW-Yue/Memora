# 行必须可导航

状态：讨论稿，待授权。

live 行提交后，必须至少被一个活跃叶子指向。零叶子的行写进去了，语义树上永远走不到，等于静默丢失。

判据是叶子上的 RowID，不是独立 membership。`exec` 直连和 Mutation Plan 走同一条 `sqlstore` 提交路径，都要拦。

## 约束的是提交后的状态

| 操作 | 约束 |
| --- | --- |
| INSERT | 结果 live 行 ≥ 1 个活跃叶子 |
| UPDATE | 缺省 `route_leaf_ids` 表示保留；显式清空则失败 |
| DELETE / 软删 | 豁免 |
| SPLIT / MERGE | 每个仍 live 的目标各自满足 |
| 摘叶子（DELETE ROUTE / MOVE） | 不能把某 live 行打成零叶子，除非同事务重挂 |
| history / `AS OF` | 豁免 |

引擎不替 Agent 选叶子。失败用 `constraint_violation`，出路只有两条：挂已有空叶子，或先 `CREATE ROUTE` 再建。

存量孤儿不追溯、不挡 reopen。先做只读报告，再上拦截。

## 拆分

1. 把上表写成可执行规格（本文扩成 ADR 或保持本页）。
2. 只读体检：精确列出零叶子 live 行，不改写入。
3. INSERT 拦截。
4. 摘叶子 / 显式清空拦截。
5. 存量出路二选一写死（隔离叶，或开库即拒）。

拦截点必须在事务提交时，不能只在 parser 或 Skill。SPLIT/MERGE 的中途态不能被误伤。
