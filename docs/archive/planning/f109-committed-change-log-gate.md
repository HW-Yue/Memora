# F109 Committed Change Log 开工与完成门

状态：已完成，完成门 PASS。

## 唯一主要结果

一个成功 logical commit 对应一个完整 durable change envelope；失败、rollback 或 crash
tail 对应零个 envelope。

## RED

1. 项目没有 change envelope 类型、checksum 或 transaction identity；
2. Row、History、Relation、Route 和 membership 虽可原子提交，但没有同事务 timeline；
3. Catalog、Configuration 和 Route node 变化无法与 Row 时间线统一排序；
4. split/merge 只能被误拆成多条对象事件；
5. crash tail、重复 cursor 或损坏 payload 没有 strict rejection 证据。

最小 RED（2026-08-01）：

```text
go test ./internal/nativechange \
  -run '^TestCommittedEnvelopeCodecRequiresTransactionAtomicity$' -count=1
FAIL: no required module provides package github.com/HW-Yue/Memora/internal/change
```

失败只因为领域与持久层能力完全缺失，不依赖随机调度或坏 fixture。

## GREEN

- canonical `change.Envelope`、entry budget、checksum 与严格 decode；
- `ObjectKindCommittedChange` 在业务 native Transaction 内 stage；
- 独立 Change Log cursor，不破坏 Row AS OF commit sequence；
- 默认 Catalog/Row/Relation/Route/Configuration/reshape 服务全部接线；
- source inventory、replacement 和 reopen 识别新 record kind。

## 不做

- F113 Page cursor index、MSQL `SHOW CHANGES` 与 Admin timeline；
- retention/compaction、replication、PITR 或旧历史回填；
- 正文 before/after image 和物理 Page 事件。

## 完成门

- [x] codec golden-like determinism、tamper、corruption 与 budget；
- [x] commit/rollback/crash-tail 原子性；
- [x] 所有默认 mutation kind 与 cross-object reference journey；
- [x] 并发 cursor 与 targeted `-race`；
- [x] full、vet、全仓 race、integration、e2e、cross-build；
- [x] 独立 commit；合入动作在本文件随 commit fast-forward 到 `main` 时完成。

完成证据：`internal/change` 与 `internal/nativechange` 覆盖 codec、tamper、corruption、
原子 commit/rollback 和 crash tail；Authority reference journey 覆盖九次多类型提交、
replacement/reopen 与 16 writer 并发；cross-object coordinator 覆盖单 envelope 与失败零
event。`scripts/ci.sh` 全绿。完成后结论：PASS。
