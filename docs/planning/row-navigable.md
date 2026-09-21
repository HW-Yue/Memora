# 行必须可导航

状态：讨论稿，待授权；**写入侧已实现**（2026-09-21，见下）。

live 行提交后，必须被**恰好一个**活跃叶子指向。零叶子的行写进去了，语义树上永远走不到，等于静默丢失。

**进度**：只读报告已落地——`memora doctor` 报出零叶子行、多叶行、挂载不一致三类计数，
可当收敛断言；INSERT／UPDATE／SPLIT-MERGE 目标已在 `sqlstore` 提交路径强制（长度 ≠ 1 即
`constraint_violation`，零写入），UPDATE 的挂载同时改为替换语义；`DELETE ROUTE` 已整条退役，
摘叶子的路径不再存在，空壳分支由引擎剪枝（[致空场景清单](../product/route-companion-table.md)）。

判据是叶子上的 RowID，不是独立 membership。`exec` 直连和 Mutation Plan 走同一条 `sqlstore` 提交路径，都要拦。

## 约束的是提交后的状态

| 操作 | 约束 |
| --- | --- |
| INSERT | 结果 live 行**恰好 1 个**活跃叶子（1:1，2026-09-21 修订） |
| UPDATE | 缺省 `route_leaf_id` 表示保留；显式清空则失败 |
| DELETE | 改为[归档后物理删除](../product/row-delete-archive.md)，行与叶子一起删，豁免本约束 |
| SPLIT / MERGE | 每个仍 live 的目标各自满足 |
| 摘叶子（引擎内部：删行、重构） | 不能把某 live 行打成零叶子，除非同事务重挂。`DELETE ROUTE` 已从 Agent 表面退役，见[分界](../query/agent-engine-boundary.md) |
| history / `AS OF` | 豁免 |

引擎不替 Agent 选叶子。失败用 `constraint_violation`，出路只有两条：挂已有空叶子，或先 `CREATE ROUTE` 再建。

存量孤儿不追溯、不挡 reopen。**存量出路已写死**（2026-09-21）：**不做数据迁移**——
违反新不变量的存量数据直接删掉，需要时删库重建。先做只读报告，再上拦截。

## 拆分

1. 把上表写成可执行规格（本文扩成 ADR 或保持本页）。
2. 只读体检：精确列出零叶子 live 行，不改写入。
3. INSERT 拦截。
4. 摘叶子 / 显式清空拦截。
5. 存量出路：直接删（不迁移、不兼容、不挡 reopen）。

拦截点必须在事务提交时，不能只在 parser 或 Skill。SPLIT/MERGE 的中途态不能被误伤。
