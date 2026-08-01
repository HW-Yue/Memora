# F107 Page Store Default Switch 开工与完成门

状态：已完成；2026-08-01 验收。

## 唯一主要结果

新实例与已迁移实例激活 live Page generation；MSQL Catalog/Row 正常读写只以三棵
Page Tree 决定可见性，Record 文件不再是查询 authority。

## RED

1. daemon 新建 `database.memora`，却没有构建/激活 Page generation；
2. native 写成功后 Page Current/Version 不变，重开读不到新 Row；
3. Catalog/Row 查询仍能调用 Record `IDs()` 全量扫描；
4. Version 与 Current 分步发布时 reader 能看到混合状态；
5. Page 发布失败后实例继续服务，或重开不能幂等修复；
6. marker、Page 或 WAL 损坏后静默退回 Record scan。

最小 RED 证据（2026-08-01）：

```text
go test ./internal/daemon -run '^TestRunReleasesLeaseAfterCancellation$' -count=1
FAIL: Page Store generation was not activated before ready: no such file or directory
```

fixture 是新的绝对 data directory；失败只因为 daemon 尚未创建并激活 F106 generation，
不是编译错误、随机时间或损坏 fixture。

## GREEN

- 实现并接线 `OpenAuthority`、durable marker、live generation open 与 startup reconcile；
- Catalog/Row 服务通过 authority read/publish 契约运行；
- daemon session 配置同一 indexed point-read lane；
- 所有 RED 变绿，并通过 targeted、full、race、vet、CI。

## 不做

- COW replacement（F108）、change envelope（F109）；
- secondary index、compaction、free-page reuse；
- Route/Relation 新物理索引；它们仍由各自现有确定性 repository 管理。

## 完成门

- [x] 规格与 marker golden/corruption corpus；
- [x] 新实例、迁移实例、reopen reference journey；
- [x] publication fault matrix 与 poisoned-process 证据；
- [x] 无旧 Catalog/Row scan fallback 证据；
- [x] concurrency/race 证据；
- [x] 全量测试、vet、format、CI 全绿；
- [x] 独立 commit 并 fast-forward 合入 `main`。

验收证据：`TestAuthority*` 覆盖 marker rename/fsync、stale generation、Catalog/Row
各发布相位、poison/reopen、坏 Record 无 fallback 与同 Row 并发；daemon 旅程覆盖
DDL/Insert/point Select/reopen。`go test ./...`、`go test -race ./...`、`go vet ./...`
和 `scripts/ci.sh` 全绿。
