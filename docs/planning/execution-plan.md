# 执行计划

状态：**2026-09-20 重写。这是当前唯一的工作队列。**

上一份队列（[2026-08 引擎侧 E 阶段](../archive/planning/execution-plan-2026-08.md)）
依据的是自研 Page/WAL/B+ Tree，代码已删除，**不再派发**。战略理由见
[路线 v3](../archive/planning/roadmap-v3.md)（引擎轨道已结束，见该文头部注记）。

每项仍须按 [TDD 协议](./feature-tdd-protocol.md) 独立 Review、授权、实现、验收。
持久化相关项测 reopen / 中断 / 错误注入即可；SQLite 自己的页格式与崩溃恢复不测。

## 已裁定、不再争论

| 决定 | 结论 |
| --- | --- |
| 存储基座 | SQLite 普通表，不自研引擎（[ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)） |
| 检索四条路 | **产品形态保留**：语义索引是 Agent 主路；关键词 + 向量是双路召回；jev 只在 Skill |
| 召回内核 | **已删**（`mem_postings` / vec0 / embedding / `REBUILD` / `reindex`）。架构下一轮再规划，本轮不发明 |
| 行关系 | 只留行上 `links` 字段；`RELATE` / `UNRELATE` / `SHOW RELATIONS` 及独立关系对象已删，读写后面重写 |
| Route / Schema Mutation Plan | **本轮不改**，下一轮再讲 |
| Row 向量 | 产品仍允许，命中直接返回叶子路径，逐段带 `route_id`（[ADR-0012](../decisions/0012-row-vector-leaf-path.md)）。当前无实现 |
| 写入侧 | 本轮不动；`branch_fanout` 硬上限保留 |
| jev | Skill 侧选择器，不进内核（[jev 选择器](../query/jev-branch-selection.md)） |
| PDF / 长文档吸收机 | **删**。不落 PDF/chunk；也不保留 Assimilation MSQL / Document IR / Source Receipt 流水线 |
| Admin 网页 | **留**。只读观察，不当写入或交互入口 |
| MCP | **留** |
| 内置 Agent 周边 | **删**。conversation journal、hostinput、feedback、`memora ask` / reflect / capture / decide |
| Semantic Health | **删**。`maintain --report` 与事后扫描 |
| ARCHIVE / UNARCHIVE / PURGE | **删**。对象归档不是产品面；废弃+接替走行生命周期 |
| 评测设施 F212–F215 | **删**。ADR-0010 的「代码冻结保留」作废 |

## 队列

现役只保留 Catalog / 数据表 / 语义树 / `SHOW ROUTES` / `SELECT`。
下面几项**都不派发实现**，只作记录，等召回架构规划完再拆 Feature。

### 召回架构（待规划）

产品仍是四条路，只出路径、事实回表。旧 Q0（vec0 按 kind 预过滤）与旧 Q2
（在已删内核上重写 MSQL 门）一并作废。新方案未定前不写代码。

### F224：Row 必须可导航

[规格](./f224-mandatory-row-route.md)仍是产品不变量：零叶子的 Row 没有路径可返回。
它不绑在已删的向量内核上。本轮不实现。

### 查询 Skill 编排

四条路怎么单走或组合，在 Skill 侧。jev 逐层 `Choice` 不改内核。
排在召回架构之后。

## 刻意不在本队列

- 写入侧 jev / 取消 fan-out 上限
- 自研引擎、三份日志、B+ Tree、MVCC
- 上一份 E 阶段未完成项（E9 盘上索引等）——对象已不存在
- 在已删的 postings / vec0 内核上修缺陷或重开 MSQL 门
