# 历史设计归档

**不是当前规格。** 日常讨论和实现不要整篇读取。当前入口是
[`../README.md`](../README.md)。

2026-09-20 把自研引擎、过时产品理念、历史 Feature 完成门从生效区整批移入此处。
当前基座是 SQLite + sqlite-vec。

按目录：

- [存储](./storage/) — Page / WAL / B+ Tree / MVCC / COW / 三份日志
- [规划与 Feature 门](./planning/) — 含 2026-08 执行计划、路线 v2/v3、F 编号账本
- [产品快照](./product/) — 旧系统能力、Database Package、质量模型
- [决策](./decisions/) — ADR-0001、0003–0006（自研 Store，Superseded）
- [查询/数据/Agent/开发](./query/) — 旧预测器、Relation Store、评测与 Admin 规格
- [早期调研](./AI_NATIVE_PERSONAL_DATABASE_RESEARCH_2026-07-29.md)
- [被取代的设计草稿](./design/README.md)
