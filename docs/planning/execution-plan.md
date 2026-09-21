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
| 1 | **落地基座** | 修 `ci.yml` 的 `CGO_ENABLED`／GOOS sweep，拿到真实绿 CI；补 `catalog`／`row`／`instance` 最小回归（**不落 `main`**） | 小，2–3 F |
| 2 | **写入不变量层** | 把「live 行**恰好**被一个活跃叶子指向」做成 `sqlstore` 提交路径上的强制检查；顺手给 `rows`／`routes` 补事务级测试骨架 | 中，3–4 F |
| 3 | **节点生命周期** | 空节点在**任何致空操作**里同事务物理删除 + 递归向上剪枝；`DELETE ROUTE` 从 Agent 表面退役；INSERT 隐式建路径。**剪枝与退役已在模块 2 的 Feature 3 落地**，本块只剩 INSERT 隐式建路径 | 中，2–3 F |
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

## 进度

- **M1 · CI 修复 ✓**（2026-09-21）：各 stage 自己决定 `CGO_ENABLED`；真实 CI 在主线分支
  两个平台全绿（run `35563180634`）；三个 devgate 门锁住这条规则。
- **M1 · 三包回归 ✓**（2026-09-21）→ 并入 M2 的 Feature 1。
- **M1 · baseline 落 `main` ✗** 作废：`main` 是另一条路（见 [`AGENTS.md`](../../AGENTS.md)「分支主线」）。
- **M2 · Feature 1 ✓**（2026-09-21）：`catalog`／`row`／`instance` 与写入路径有了回归网，
  两个变异检验证明它会咬人。
- **M2 · Feature 2 ✓**（2026-09-21）：挂载必须恰好一个——提交时长度 ≠ 1 即拒绝、零写入；
  UPDATE 的挂载由并集改为替换，被离开的叶子当场清空行指针。
- **M2 · Feature 3 ✓**（2026-09-21）：`DELETE ROUTE` 整条退役（parser 给指路诊断，AST／
  executor／安全分类／`deprecateNode` 一并移除）；引擎在 route mutation 应用后剪掉空壳分支
  并递归向上，空叶子与带 `successor_ids` 的废弃节点不剪。
- **M2 · Feature 4 ✓**（2026-09-21）：`doctor` 报三类违例——零叶子行、多叶行、挂载不一致；
  不变量因此有了可复跑的收敛断言。**M2 完成。**
- **下一件（待定序）**：**不变量断言收口**——顾问在主线对齐检查里点出的唯一欠账：把 doctor
  的三条查询提成引擎内部断言，测试环境每个事务提交前跑一次、计数必须为 0，生产退化为
  `doctor` 命令；否则 M3／M4 制造的违例要拖到 M6 才发现。之后才是 **M3 INSERT 隐式建路径**。

## M2 的 Feature 切分（已完成）

写入不变量层：**live 行恰好被一个活跃叶子指向**。四个 Feature 全部完成，顺序即依赖。

| # | 主要结果 | 验收 |
|---|---|---|
| 1 | **写入路径地基回归** | `catalog`／`row`／`instance` 有能跑的单元测试，覆盖挂载解析、行落盘、事务边界。只覆盖 F2–F4 要改的函数，不追求全覆盖——不变量改的正是这三包的语义，没有基线就分不清「新约束生效」还是「老路径本来就坏」 |
| 2 | **挂载必须恰好一个** | 提交时 live 行的挂载长度 ≠ 1（空或多）整体拒绝、零写入。空数组与多叶一次关掉；**UPDATE 的挂载从并集改成替换语义**（见下）；存量违例按既定策略删库重建 |
| 3 | **致空操作自动剪枝** | 任何致空操作在同一事务内剪掉空节点并递归向上；绝不产生零叶子 live 行。`DELETE ROUTE` 从 Agent 表面退役 |
| 4 | **`doctor` 报不变量违例** ✓ | 报出零叶子行、多叶行、挂载不一致三类计数，成为后续 Feature 的收敛断言。「一叶多行」在当前表结构下不可表示（叶子只有单个 `row_id`），可观察的等价物是「行声明的叶子不回指它」，即第三类 |

**已识别的坑**：Feature 2 的校验必须是**提交前的一次性检查**，不能放在字段赋值处——
UPDATE「先清空再重挂」的中间态必然短暂为空，提前校验会误杀合法操作。

**Feature 1 已经挖出来的一个真问题**：行级 UPDATE 的挂载现在是**并集**（`mergeLeaves`），
不是替换——`[]` 是空操作、给第二个 leaf 会累加成两个。所以「非 nil 空数组显式清空」
这句在行级路径上一直不成立（已修正 [MSQL Mutation](../query/msql-mutation.md)）。
Feature 2 要把并集改成替换，否则 1:1 永远立不住。基线测试
`TestUpdateMountIsAUnionNotAReplacement` 钉住了现状。

**Feature 3 开工前必须先定死一件事**：某操作**将要**让一个 live 行失去全部叶子时，是
拒绝（`constraint_violation`），还是允许并要求同事务补齐？现规格
（[行必须可导航](./row-navigable.md)）写的是后者：**拒绝，除非同事务重挂**。
F3 动手前先把致空场景穷举成一页清单，否则 reopen／中断证据会反复推翻实现。

**过渡形态**：`route_leaf_ids` 暂保持数组但**恰好一项**；单数名 `route_leaf_id` 与外部
契约版本一起改（[写入形态](../product/write-model.md) §1.3 写的是目标形态）。

## 三条风险

1. **真实 CI**：M1 已经跑通一次；此后每个 Feature 合回主线前都要重跑，别再出现「本地绿、
   CI 红」；
2. **`sqlstore` 写入路径 1500+ 行只有 249 行测试**——模块 2–4 是在没有回归网的代码上改事务
   语义，所以 M2 的 Feature 1 先铺网；违反 1:1 的存量按既定策略直接删；
3. **模块 7 体量最大却方案未定**——别让它的不确定性回压前六块的接口设计。

Route / Schema Mutation Plan 本轮不改。
