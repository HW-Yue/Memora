# 执行计划

状态：2026-09-21。现役唯一工作队列。里程碑、判据与顺序理由见[项目计划](./project-plan.md)。

工作项用题目。`F1`–`F228` 只在 `docs/archive/`。分支 `feature/<short-name>`。
每项按 [TDD](./feature-tdd-protocol.md) 独立 Review、授权、验收。

## 现在有什么

SQLite 一个文件：Catalog、数据表、history、语义配套、`mem_changes`、配置。
查询主路：`SHOW ROUTES` → `OPEN ROUTE` → `SELECT`。
写入：`INSERT` / `UPDATE` / `DELETE` / `SPLIT` / `MERGE`，叶子挂 RowID。
接入：CLI、MCP、只读 Admin、Skill。

## 还要写什么：七块，按顺序

规模是相对量级与大致 Feature 数（一个 Feature = 一个主要结果）。

| # | 模块 | 职责 | 规模 |
|---|---|---|---|
| 1 | **落地基座** | 修 `ci.yml` 的 `CGO_ENABLED`／GOOS sweep，拿到真实绿 CI；squash 一个 baseline 落 `main`；补 `catalog`／`row`／`instance` 最小回归 | 小，2–3 F |
| 2 | **写入不变量层** | 把「live 行**恰好**被一个活跃叶子指向」做成 `sqlstore` 提交路径上的强制检查；顺手给 `rows`／`routes` 补事务级测试骨架 | 中，3–4 F |
| 3 | **节点生命周期** | 空节点在**任何致空操作**里同事务物理删除 + 递归向上剪枝；`DELETE ROUTE` 从 Agent 表面退役；INSERT 隐式建路径 | 中，2–3 F |
| 4 | **归档式删除** | 六步单事务：归档、删行、删叶、剪空父、按 `links` 倒推摘对端、删 history；恢复交给 Agent | 大，4–5 F |
| 5 | **history 谱系** | `superseded` 不推进 revision；新行首条 history 带来源指针（MERGE 为列表）；与 `successor_ids` 并存 | 中，2–3 F |
| 6 | **行链接读写** | `links` 开放读写：双向一致、摘要懒更新、删除级联摘除（级联另有界、如实报数） | 中偏大，3–4 F |
| 7 | **召回 + 查询 Skill 编排** | 关键词 + 向量只返路径，四条路单走或组合 | 大，5–6 F，含选型 |

**顺序理由**：模块 3 必须在 4 之前（删除依赖剪枝语义）；6 在 4 之后（级联摘除靠删除事务
已成形的两面一致）；7 放最后（方案未定，且依赖前面结构稳定）。

合计约 **22–28 个 Feature**。

## 规格在哪

- 模块 1：[决策日志](../decisions.md)「落地顺序」；模块 2：[行必须可导航](./row-navigable.md)
- 模块 3：[Agent 与引擎的分界](../query/agent-engine-boundary.md)、
  [Route 配套表](../product/route-companion-table.md)「重构与废弃节点」
- 模块 4：[行删除](../product/row-delete-archive.md)；模块 5：[history 谱系](../product/history-lineage.md)
- 模块 6：[行链接](../product/row-links.md)
- 模块 7：[查询形态](../product/query-model.md) §6、[检索路线](../query/retrieval-routes-jev.md)

产品形态总纲见 [写入](../product/write-model.md) 与 [查询](../product/query-model.md)。

## 三条风险

1. **34 个提交从未见过真实 CI**——模块 1 之前所有「绿」都不可信；
2. **`sqlstore` 写入路径 1500+ 行只有 249 行测试**——模块 2–4 是在没有回归网的代码上改事务
   语义。1:1 不变量很可能暴露既有数据或既有写路径本来就违反它，届时要么加迁移，要么放宽规格；
3. **模块 7 体量最大却方案未定**——别让它的不确定性回压前六块的接口设计。

Route / Schema Mutation Plan 本轮不改。
