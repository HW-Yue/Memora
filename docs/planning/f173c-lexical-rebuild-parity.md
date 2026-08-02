# F173c：Lexical Rebuild 与 Snapshot Parity

规划状态：已批准；2026-08-03 已完成实现前 Review，用户连续执行授权覆盖本项。

## 唯一主要结果

提供一个正式、可观测、可恢复的全量 lexical index rebuild：从同一 native snapshot 重建 v3
generation，验证 Catalog、Current Row、Row Version 与 Fulltext reference parity 后原子切换，并通过
MSQL 返回新 generation 与规范化 Fulltext snapshot 摘要。

## MSQL 契约

固定语法：

```sql
REBUILD LEXICAL INDEX
```

这是 instance-wide、仅 autocommit 的 L2 structural maintenance；显式事务内稳定返回 unsupported，
不回滚用户 Row 事务。成功结果固定返回一行：

- `previous_generation`、`generation`、`epoch`、`plan_digest`、`source_fingerprint`；
- `previous_snapshot_sha256`、`snapshot_sha256`、`parity`、`verified`、`reused`。

命令不接受 Database、路径、索引实现或 Provider 参数，不暴露 posting、Row 正文或对象数量。

## 物理策略

复用 `Authority.ReplaceGeneration` 的 write gate、staging build、source reverify、strict reopen、marker
publication 和旧 generation 保留语义。虽然用户目标是 lexical rebuild，物理上仍替换完整四树 generation，
避免 Fulltext 与 Catalog/current snapshot 脱离同一个 marker；不新增 in-place repair 或第二套 swap 协议。

Authority 在 gate 内读取旧 Fulltext 的规范快照，构建并验证新 generation，再计算新快照：

```text
snapshot = version + sorted live Objects + sorted Postings
sha256 = SHA-256(canonical JSON(snapshot))
```

删除/取代 tombstone 是代内 revision guard，不属于可查询 lexical snapshot，因此不进入摘要；新 generation
仍由 reference verifier 独立验证所需 tombstone。`verified=true` 只表示新 generation 已与 native Plan/reference 全量对拍；`parity` 表示旧在线 logical
snapshot 与新 snapshot 摘要相同。旧索引缺失或不可读时允许 rebuild 修复，旧摘要为空且 parity=false；
新快照无法读取或 reference 不一致时禁止 marker 切换。

## 依赖边界

- Parser/AST 只表达维护意图；Executor 依赖窄 `LexicalIndexMaintenance` port；
- native daemon 将 Page Authority 注入该 port；Agent 仍只能通过同一个 MSQL 入口调用；
- Page Store 不依赖 MSQL，逻辑 Row/Catalog 实现没有 maintenance 能力时稳定返回 unsupported；
- Fulltext 仍是派生位置，rebuild 不写 native body、Change Log 或语义 revision。

## Failure matrix

| 证据 | 边界 | 稳定结果 |
| --- | --- | --- |
| parser/AST | 精确语法、尾随 token | 唯一 statement kind 或 parse failure |
| executor fake | receipt、缺失 port、显式事务 | 稳定 envelope；无物理层 import |
| parity | Catalog/Row/Route mixed revisions | 新摘要与 reference 一致；健康旧树 parity=true |
| repair | native truth 比在线 posting 更新 | parity=false；swap 后命中完整 |
| strict verify | 新 generation posting 偏差 | marker 不切换 |
| replacement fault | build/rename/fsync/marker/reopen | 复用现有故障矩阵与 outcome unknown |
| daemon e2e | MSQL → Authority → reopen | receipt 可见；新 marker 持久化 |
| concurrency/race | rebuild 与 reader/writer | gate 串行写；reader 只见完整 generation |

## 产品门审计

- 不增加模型调用、上下文、Agent、向量、HNSW、文档 chunk 或查询答案入口；
- 不删除旧 generation；回收仍需独立 Feature，不藏进 rebuild；
- F174 才提供有界 lexical location query；F173c 只交付维护和一致性证据；
- 一个主要结果、一个故障域，开工前结论：PASS。

RED 入口：`go test ./internal/msql/parser ./internal/msql/executor ./internal/pagestoremigration ./internal/daemon`。

## 关联

- [F173b2](./f173b2-live-route-posting-publication.md)
- [Generation v3](../storage/page-index-generation-v3.md)
- [TDD 协议](./feature-tdd-protocol.md)
