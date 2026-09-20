# 开发计划（过程稿）

状态：**过程稿，不是终稿。** 按块追加，不覆盖已有块；每块的结论来自「已核实事实 + 一次顾问短咨询」。
各块过完、覆盖到整体后，再整理成一份完整项目计划。

---

## 块 1 · 2026-09-20 · 写入不变量：Row 必须可导航

来源：旧编号 F224（`docs/planning/mandatory-row-route.md`，2026-08-11 的候选，
自述「尚未 Review、尚未获得实现授权」且「需按新形态重写」）。**本块不再沿用该编号**——
F 编号是自研引擎时代的 Feature 序列，现行文档引用了 93 个不同编号，但只剩 4 个还有实体文档，
代码里完全不认识它们。本块用一句话规格命名。

**结论**：下一块做它，不做召回、不做行链接。
**为什么是它**：只有它有**时间成本递增**属性——它是数据不变量，每多跑一天就多攒一批
零叶子 live Row，事后回填比事前拦截贵一个量级。关键词/向量召回方案未定（且 ADR-0012
明确它是 Row 向量召回的硬前置）；行链接是纯新增能力，晚做只损失功能、不损失数据；
Route root 可发现性是读侧缺口，任何时候补代价不变。

**主要结果**：任何提交后处于 live 的 Row，必须至少有一个 active Route leaf；
会产生零归属 live Row 的写入一律失败，返回结构化信封与可执行出路。

### 拆分（严格按序，每步独立可验收）

| # | 步骤 | 边界 | 验收 |
|---|---|---|---|
| 1 | **定义不变量语义（只写文档）** | live Row ⇒ ≥1 active leaf；列清豁免：软删后的 Row、history 行、`AS OF` 读出的旧版本、SPLIT/MERGE 事务中途态 | 一张「操作 × 是否受约束 × 违反时行为」表，进 ADR |
| 2 | **只读体检器**（`admin` 或 `memora check`，**不改写入路径**） | 只报告，不修 | 能在实测的 probe 库上精确捞出那行零叶子 live Row。必须在任何拦截之前——它同时是后续测试的 oracle 和第 5 步的输入 |
| 3 | **INSERT 侧拦截** | 提交时校验，不是逐操作校验 | 不带 `route_leaf_ids` 的 `exec` 直连 INSERT 返回明确类型化错误；被打破的既有测试显式补叶子 |
| 4 | **叶子摘除侧拦截** | DELETE ROUTE / MERGE / UPDATE 把某 Row 打到零叶子时拒绝，或要求同语句内重挂 | 三条路径各一个独立用例 |
| 5 | **存量库迁移** | 给已有孤儿 Row 一条确定性出路（隔离叶 `/unrouted`，或开库即拒 + `repair`），**二选一写死** | 迁移后体检器返回空 |

### 最大风险

1. **拦截点放高了。** 落在 `msql/service` 或语法层，`exec` 直连那条路照样绕过去——
   实测已证明它是洞。必须落在 `sqlstore` 的**提交路径**这一个咽喉上，
   且是**事务提交时**校验而非逐操作校验，否则 SPLIT/MERGE 的中间态会把 Reshaper 卡死。
2. **第 1 步把豁免列表写窄了**，history / `AS OF` 被误伤，会在第 4 步才爆出来，
   代价是回头改语义。

---

## 已核实的事实（供后续块复用）

均为 2026-09-20 在 `rewrite/adr0011@2da2b18` 上实测，不是转述文档。

1. **F224 前提成立**：`exec` 直连发 `INSERT INTO probe.notes(title,body) VALUES(...)`
   不带 `route_leaf_ids`，**提交成功**，Row live 且零 Route 归属。`UPDATE` 要求 `expected_revision`。
2. **F228 代码里已完成，文档落后**：`internal/sqlstore/catalog.go` 在同一事务里建
   `_memora_routes_<tableID>` / `_memora_history_<tableID>`；`SHOW TABLES` 不返回它们；
   Agent 直接 `SELECT * FROM probe._memora_routes_<id>` 报 not found。
   但归档前的 `docs/planning/f228-route-companion-table.md` 仍标「待授权开工」。
3. **召回在代码里零残留**：grep `lexical` / `posting` / `vec0` / `embedding` 全空。
4. **行链接半成品**：行上已有 `links` JSON 字段，`sqlstore/links.go` 有 `RowLinks()` 读 +
   内存内过期摘要刷新 + `summarize()`；但 `Link` 结构体残留
   `RelationID/Direction/Type/Description`（`docs/product/row-links.md` 明确说不设类型），
   摘要上限写死 280 字符（文档说不设上限）；**MSQL 读写语句完全没有**。
5. **Route root 不可发现**：`SHOW ROUTES FROM TABLE x AT ROOT` 返回 root 的**子节点**
   （path `/first`），root 自己（path `/`，kind `root`）任何语句都不返回，
   `DESCRIBE TABLE` 也不暴露 root id → root id 只在 `CREATE ROUTE ROOT` 的响应里出现一次，
   daemon 重启后拿不回来。导航本身不需要它（`AT ROOT` 直接给第一层），
   但**新建第一层节点**需要。
