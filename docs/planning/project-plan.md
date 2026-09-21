# Memora 项目计划

状态：**正式计划**（2026-09-21 整理，用户授权）。本文件管**里程碑、判据、顺序与规模**；
逐项工单仍在[执行计划](./execution-plan.md)——它是唯一队列，不在这里重复。

## 一、目标与验收判据

Memora 是给 AI Agent 用的本地个人数据库：Agent 自己建模、用 MSQL 读写，通过**语义树**
逐层定位，再用 `SELECT` 回表取事实。持久化基座是 SQLite，一切产品结构都是普通表
（[ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)）。

**这套东西算做完，按四条判据核对**（不看「命令能跑」）：

1. **准则符合性**：产品宪章、写入形态、查询形态三份最高准则逐条可核对为已做到；
2. **冷启动旅程**：一个没有旧聊天、没有长期 prompt 的 Agent，只在有界输出内完成——
   发现库表 → 逐层导航 → 写入一条语义模块 → 从顶层重新找到它 → 原地修改 →
   归档式删除 → 从归档重建；
3. **不变量**：live 行**恰好**一个活跃叶子；一次逻辑写入一个事务；删除后从任何读面
   （含 `AS OF`）都拿不到；召回只返回路径，不返回分数、理由与正文；
4. **质量门**：`./scripts/ci.sh` 六道门在**真实 CI** 上对 `main` 全绿；每个 Feature
   独立可回滚。

**明确不做**：自研存储引擎／MVCC／WAL 与恢复重放；把文档 chunk、PDF、图片作为持久化
内容；以分数、距离、排名或正文作为答案；以扩大评测样本替代修导航。

## 二、基线

SQLite 一个文件：Catalog、数据表、history、语义配套、`mem_changes`、配置。
现役查询主路 `SHOW ROUTES` → `OPEN ROUTE` → `SELECT`；写入 `INSERT` / `UPDATE` /
`DELETE` / `SPLIT` / `MERGE`；接入 CLI、MCP、只读 Admin、Skill、SDK、daemon。
源码约 22.5k 行、测试约 5.4k 行。分支 `rewrite/adr0011` 领先 `main`，**从未跑过真实 CI**。

## 三、里程碑

### M1 落地基座 —— 小，2–3 F

修 `ci.yml`：`CGO_ENABLED=1` 下沉到需要 cgo 的 stage，`GOOS=linux` sweep 用 0 →
在分支上见一次真实绿 CI → squash 一个 baseline 提交落 `main`、分支留 tag 存档 →
补 `catalog`／`row`／`instance` 最小回归。

**判据**：GitHub CI 对 `main` 六道门全绿；baseline 可构建可回滚；三个核心包有可跑回归。

### M2 写入不变量层 —— 中，3–4 F

把「live 行**恰好**被一个活跃叶子指向」做成 `sqlstore` 提交路径上的强制检查
（不是 parser、不是 Skill）；先只读体检，再上拦截；顺手给 `rows`／`routes` 补事务级测试骨架。

**判据**：INSERT 不带叶子必失败；UPDATE 显式清空必失败；摘叶子不能把 live 行打成零叶子。
**证据**：reopen、中断、错误注入。**存量**：不迁移，违反的存量直接删（[行必须可导航](./row-navigable.md)）。

### M3 节点生命周期 —— 中，2–3 F

空节点在**任何致空操作**里同事务物理删除并递归向上剪枝；`DELETE ROUTE` 从 Agent 表面
退役；INSERT 允许隐式建路径。见 [分界](../query/agent-engine-boundary.md)、
[Route 配套表](../product/route-companion-table.md)。

**判据**：删掉最后一行后树上不留空壳；根变空则 `router_root_id` 置空、`AT ROOT` 返回空页；
带 `successor_ids` 的废弃节点不被清空规则删掉。

### M4 归档式删除 —— 大，4–5 F

一个事务六步：归档（删除前的语义路径 + 内容）、删行、删叶、自底向上剪空父、
按行上 `links` 倒推摘掉对端那一面、删 history。级联另有界并如实报数。恢复不由引擎做。
见 [行删除](../product/row-delete-archive.md)。

**判据**：删除后点查／列表／`OPEN ROUTE`／`SHOW HISTORY`／`AS OF` 全部拿不到；
归档自足到 Agent 拿它就能重建；删除后库里无悬空链接；级联超界拒绝而非半写。

### M5 history 谱系 —— 中，2–3 F

源行标 `superseded` 时 **revision 不推进**；新行首条 history 带来源指针（MERGE 为列表）；
与源行 `successor_ids` 并存。见 [history 谱系](../product/history-lineage.md)。

**判据**：`(row_id, revision)` 不出现没有记录的空洞；从任一新行能顺指针回到身份变化之前；
`AS OF` 对 `superseded` 源行的行为写死并有测试。

### M6 行链接读写 —— 中偏大，3–4 F

`links` 开放读写：双向一致、摘要懒更新、删除级联摘除（有界、报数）。
见 [行链接](../product/row-links.md)。

**判据**：任何写都两面一致；摘要过期可识别、可刷新且照常写 history；删除不产生悬空链接。

### M7 召回 + 查询 Skill 编排 —— 大，5–6 F（含选型）

关键词与向量两条召回**只返回语义路径**，事实一律 `SELECT` 回表；四条路在 Skill 层单走或组合，
jev 不进内核。见 [查询形态](../product/query-model.md) §6、[检索路线](../query/retrieval-routes-jev.md)。

**判据**：召回响应里没有分数、距离、理由、正文；命中路径可直接接回逐层导航；
关键词与向量的可见性口径写死。

## 四、顺序与依赖

`M1 → M2 → M3 → M4 → M5 → M6 → M7`。

- M3 必须在 M4 前：删除依赖剪枝语义；
- M6 在 M4 后：级联摘除靠删除事务里已成形的两面一致；
- M7 最后：方案未定，且依赖前面结构稳定——**不让它的不确定性回压前六块的接口**。

## 五、风险

1. **34 个提交从未见过真实 CI**——M1 之前所有「绿」都不可信。先压掉它，代价很小。
2. **`sqlstore` 写入路径 1500+ 行只有 249 行测试**——M2–M4 是在没有回归网的代码上改事务语义。
   1:1 不变量很可能当场暴露存量或既有写路径本来就违反它；存量按既定策略直接删。
3. **M7 体量最大却方案未定**——选型（词法索引形态、向量来源与盘上索引）要在 M7 开工前单独定，
   不夹带进实现。

## 六、工作方式

- 不在默认分支开发；一个 Feature 一个主要结果，独立分支、独立 Review 与授权；
- RED → GREEN → REFACTOR；碰持久化／事务／索引的必须有 reopen、中断、错误注入或 race 证据；
- 出库格式与契约的变更（`route_leaf_ids` 单数化、`DELETE ROUTE` 退役、INSERT 隐式建路径）
  要与契约版本一起走；
- **每个可独立验证的小步立即提交并推送**（本会话的容器重启会丢本地未推的东西）。

## 七、规模与时间线（粗估）

| 里程碑 | Feature 数 | 纯开发 |
|---|---|---|
| M1 落地基座 | 2–3 | 0.5–1 天 |
| M2 写入不变量层 | 3–4 | 1–2 天 |
| M3 节点生命周期 | 2–3 | 1 天 |
| M4 归档式删除 | 4–5 | 2–3 天 |
| M5 history 谱系 | 2–3 | 1 天 |
| M6 行链接读写 | 3–4 | 1.5–2 天 |
| M7 召回 + 编排 | 5–6 | 3–5 天起 |
| **合计** | **22–28** | **10–16 天**，含评审与返工按 3–4 周看 |

## 八、待定

- M6 的级联上限数值与是否进库内配置；
- M7 的关键词索引形态与向量来源（谁出向量、盘上索引用什么）；
- 归档表进[查询形态 §7](../product/query-model.md) 可达性清单的具体写法；
- 契约版本号怎么走（单数化与退役同批还是分批）。

## 关联

- [执行计划](./execution-plan.md) — 唯一队列 · [TDD](./feature-tdd-protocol.md) ·
  [产品门](./feature-product-gate.md) · [决策日志](../decisions.md)
- 产品：[写入形态](../product/write-model.md) · [查询形态](../product/query-model.md) ·
  [架构原则](../product/architecture-principles.md) · [产品宪章](../product/ai-native-product-charter.md)
