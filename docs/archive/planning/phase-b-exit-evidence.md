# Phase B 退出验收

状态：2026-07-30 机械闭环测试曾通过；不再代表语义查询产品门通过。F21–F23
与 Database 级 Router 只作历史证据，必须按产品宪章返工。

## 固定数据集

Integration gate 以 `memora.logical-snapshot/v1` 和固定 ID/时间生成：

- 1 Database；
- 1 Table、1 TEXT Column；
- 10,000 条 live Row；
- 10,000 条 INSERT History；
- 连续 commit sequence 1–10,000。

测试不读取用户目录、不依赖执行顺序、不访问网络。

## 已验证链路

`TestPhaseBExitTenThousandRowsRebuildRestartAndSnapshotHash` 覆盖：

1. v1 snapshot 导入、导出到第二 Store、canonical SHA-256 相同；
2. 10,000 Row 有界分页、首尾稳定 ID 和精确回表；
3. 同 transaction UPDATE + INSERT 共用 commit sequence；
4. rollback 后 Row、revision 和索引修改均不可见；
5. revision-guarded DELETE 与追加 History；
6. Agent、mechanical、Router 全部派生 bucket 删除；
7. 从 Row/Catalog 与新语义输入重建三类检索索引；
8. MATCH 与 Router leaf 返回稳定 Row locator；
9. SQLite 原型 Store 关闭重开后 Row、History 和重建索引仍可用。

F26 的独立兼容测试另行覆盖 Row/History/Relation 定位索引不进入 snapshot、导入时重建，以及未知字段和旧 v0 fixture。

## 进程级闭环

F27 E2E 使用真实 binary 与 daemon 完成：

```text
init → doctor → DDL → Route → INSERT
→ MATCH / OPEN ROUTE → SELECT → UPDATE
→ SHOW HISTORY → stop/start → SELECT → doctor
```

CLI 全程只走 Unix socket、`msql.execute` 和统一 Result Envelope，不直接打开 Store。

## 门禁命令

```text
go test -tags=integration ./tests/integration \
  -run TestPhaseBExitTenThousandRowsRebuildRestartAndSnapshotHash
go test -tags=e2e ./tests/e2e \
  -run TestLocalDatabaseVerticalSliceThroughCLIAndDaemon
./scripts/ci.sh
```

## 当前结论

Catalog、Row、History、Relation、snapshot、IPC 与重启证据仍有效。混合倒排、
MATCH 和 Database 级 Router 只证明旧代码可执行，不证明产品形态正确；不得
据此继续推进 F52。
