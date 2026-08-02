# F174：有界 Lexical Location 查询

规划状态：已通过单项 Review，批准按 RED → GREEN → REFACTOR 实现。

## 唯一主要结果

通过 MSQL 返回当前授权 scope 内的全内容 lexical 位置，使 Agent 能先获得 Database/Table/Route/Row
候选，再用既有 MSQL 读取事实。F174 不返回答案，不实现 Bootstrap、Agent、Vector 或后台优化器。

## 用户故事

- Agent 用一条有界 MSQL 同时发现相关语义索引和 RowID 位置；
- 调用方可以用 opaque cursor 继续同一候选快照，不能跨 query/scope 复用；
- 受限 Agent 的查询不会扫描、统计或暴露未授权 Database 的 posting；
- Row 修改、删除、Route/Catalog 修改和 rebuild 后，查询只反映当前 live generation。

## 冻结契约

语法、结果、排序、预算和回表约束见 [Lexical Locations v1](../query/lexical-locations-v1.md)。
实现分三层：

1. `fulltextindex` 增加 `(term, database_id)` scoped prefix read；
2. 中立 location domain 负责 tokenizer、聚合、稳定排序、byte budget 与 cursor；
3. Executor 先解析可见 Catalog scope，再经窄端口调用；daemon adapter 只做组合。

Agent 和 MSQL Executor 不 import Page/B+ Tree 私有实现。F124b Route discovery 不扩展 Row 字段。

## RED 证据

- Parser 接受固定语法，拒绝缺少 `ALL TABLES`、`LIMIT` 或 `BYTES`；
- scoped store 测试证明只返回指定 database_id，空 scope 不触发全库读取；
- location domain 覆盖多 term/field 聚合、确定性排序、中文 token、LIMIT/BYTES、cursor 篡改与快照漂移；
- Executor fake 记录传入的 database_id，证明 Catalog 授权过滤先于 posting read；
- native daemon 旅程覆盖 Row 与 Route 命中、SQL 回表、更新/删除、reopen 与 rebuild parity；
- lexical 故障不得退回 Row scan。

## 完成门

- 受影响包 RED 后 GREEN；
- `go test ./...`、`go test -race ./...`、`go vet ./...`；
- integration、e2e、cross-build；
- 更新当前产品、Feature 状态和后续计划后才合入 `main`。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F174 范围。

开工前结论：PASS。
