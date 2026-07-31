# F102 MSQL Point-Get 开工与完成门

状态：已完成，PASS。

## 产品门

- 目标故事：`US-QUERY`、`US-HISTORY`；AI 使用相同 MSQL，以稳定 RowID 得到相同
  当前或历史语义模块，物理读取不再随全库 Catalog/Row revision 数线性增长。
- 标准旅程：精确 SELECT → indexed Catalog → current/version locator → exact Row body →
  原 Result Envelope。
- 影响范围：只读 autocommit MSQL point-get；不改变 Row、Route、History 或 Schema。
- 上下文：不增加模型节点、RowID 或正文；纯引擎内部优化。
- 永久边界：Router 仍是语义权威；B+ Tree 只执行 Agent 已表达的确定性 SQL。
- 架构确认：F98–F100 已批准；F102 只接读路径，F107 才切唯一默认 authority。
- 用户执行授权：F81–F109 持续授权已记录。
- 唯一主要结果：配置 indexed reader 后，精确 MSQL SELECT 只走新索引且 envelope 等价。
- 开工前结论：PASS。

## RED matrix

| Case | 当前缺口 | 期望 |
| --- | --- | --- |
| current success/miss/delete | Executor 没有 indexed reader | 等价 row/空 rows，旧 Get 不可达 |
| AS OF revision/commit | 仍调用旧 repository scan | version index 定位且 envelope 等价 |
| Catalog name/alias | Describe 重组完整 Catalog | Table-scoped B+ Tree + exact record |
| extra AND predicate | 切 lane 后可能跳过过滤 | 仍执行完整 WHERE |
| locator/body mismatch | 可能返回错误对象 | corruption，无 fallback |
| reopen | 仅内存 fixture 可能掩盖接线错误 | 三棵树与正文重开后结果一致 |
| Batch envelope | Engine output 可能等价但 JSON 漂移 | statement/top-level envelope 等价 |

RED 命令：

```text
go test ./internal/msql/executor ./internal/nativecatalog ./internal/nativerow
```

首个 RED 必须因 indexed point reader 契约或实现缺失而失败。

## 完成门

- targeted repetition、全仓 unit/vet/race/CI 全绿；
- real native Record File + real F98/F99/F100 B+ Tree 的 success/miss/delete/as-of；
- legacy poison/call counter 证明 indexed lane 不调用旧 Describe/Get/AsOf；
- reopen 与 locator/body corruption 传播，无 scan fallback；
- 文档状态、计划下一项和完成证据同步；
- 完成后结论：PASS。

## 完成证据

- RED：`go test ./internal/msql/executor` 仅因
  `NewBatchSessionWithPointReads` 契约缺失失败；
- real `database.memora` 与 F98/F99/F100 三棵树覆盖 current success、missing、
  deleted、AS OF revision/commit，并在未 flush 的 committed Page 重开恢复后保持相同结果；
- legacy 与 indexed Batch Envelope 逐字节 JSON 等价，额外 AND predicate 仍完整求值；
- poison counter 在 success、not-found 与 corruption 下均为 0；locator/body 不一致
  返回 corruption，AS OF 未命中返回稳定 `not_found`；
- Table-scoped Column cursor 跨 leaf 与 reference model 一致，并去重 name/alias；
- `go test -count=20`、受影响包 `go test -race`、`go test ./...`、`go vet ./...`、
  `go test -race ./...` 与 `./scripts/ci.sh` 全部通过；
- 剩余边界：explicit transaction snapshot 属于 F103；迁移与 daemon 默认切换属于
  F105–F107，不是 F102 的隐藏 fallback。
