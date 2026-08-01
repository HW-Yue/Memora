# 原生闭环后续 Feature Review 稿

状态：已批准；2026-07-30 起按小闭环逐项实现并合入 `main`。

## 总原则

1. 每个 Feature 必须形成自己的 `write → close/reopen → read` 闭环；
2. 一次只接一种逻辑对象或一条用户旅程；
3. F53–F61 不实现事务与崩溃恢复；
4. 所有对象能在原生文件跑通后，F62 才开始事务；
5. SQLite 在迁移、回读和对照完成前保留，不边写新底层边删除退路；
6. Page、B+ Tree、MVCC、Undo/Redo、Binlog、Buffer Pool 不在本轮承诺；
7. 每项生产代码目标不超过约 400 行，超出就继续拆 Feature；
8. 每项合入前都执行产品故事门、feature 测试和全量 CI。

## 顺序总览

| 阶段 | Feature | 得到的闭环 |
| --- | --- | --- |
| 真实数据 | F53a–F55 | Typed payload、Catalog、Row、revision/tombstone 能写入并读回 |
| MSQL 接入 | F56–F57 | MSQL 建模、INSERT 和 SELECT 跑在原生文件上 |
| 对象接宽 | F58–F61 | 修改、History、Relation、Table Router 分别跑通 |
| 正确性 | F62–F65 | 事务帧、恢复、跨对象原子变更、split/merge |
| 迁移退出 | F66–F69 | snapshot 对照、默认切换、删除 SQLite |
| 产品主路 | F70–F72 | AI 逐层 Table Router、删除旧检索、故事发布门 |

依赖只允许向右推进：

```text
F53a → F53b → F54 → F55 → F56 → F57
                         ↓
                    F58 → F59 → F60 → F61
                                           ↓
                                      F62 → F63 → F64 → F65
                                                               ↓
                                                          F66 → F67 → F68 → F69
                                                                        ↓
                                                                   F70 → F71 → F72
```

## 强制停止条件

- 任一 Feature 没有 reopen 后的真实读取证据，不进入下一项；
- 为接现有代码而准备重新做永久 generic bucket/KV 层时，停止并重新 Review；
- F53–F61 若被迫依赖事务或恢复语义，不暗中加实现，停止并调整顺序；
- F56/F57 若超过单 Feature 规模，拆接口与旅程，不能用大 adapter 一次接完；
- F68 前所有 native MSQL 只运行隔离测试数据，不切用户默认 datadir。

## Review 重点

请重点判断：

1. Catalog 与 Row 是否应保持 F53/F54 两个小 Feature，不合并；
2. 是否同意到 F61 所有主要对象完成 reopen 闭环后，才开始 F62 事务；
3. 是否同意 SQLite 到 F68 默认切换成功后才在 F69 删除；
4. 是否同意 Table Router 的物理闭环放在 F61，AI Skill 主查询改造放在 F70；
5. 用户此前所说的“socket”若指 Unix socket/IPC，它不属于本计划，需另开方案；
   若指 SQLite，则由 F66–F69 完整覆盖。

## 详细规格

- [F53–F61：真实数据与 MSQL 闭环](./native-features-data-review.md)
- [F62–F72：正确性、迁移与产品主路](./native-features-transition-review.md)

本计划已经 Review 通过；它取代旧
[Phase D 计划](./tdd-phase-d-release-kernel.md) 中冲突的 Feature 顺序，并逐项生成实现证据。
