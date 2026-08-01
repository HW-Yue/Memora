# F98 Catalog Lookup 开工与完成门

状态：PASS（2026-08-01）。

## 产品审查

用户结果：按库、表、列的名称、别名或稳定 ID 做 Describe/绑定时，先用持久化
B+ Tree 得到对象位置和当前 Schema revision，不再为一次点查重读整个 Catalog。

依赖：F90–F97d3 已完成。后继 F102 才把 MSQL point lookup 切到本索引，F107 才
切换唯一存储 authority。

## RED matrix

1. 当前代码不存在持久化 Catalog locator key space，点查只能从完整快照线性寻找；
2. name/alias/ID 与父 scope 精确查询缺失；
3. 重启后没有 committed root 可继续点查；
4. 重名、别名、稳定 ID 冲突没有索引事务级拒绝；
5. 索引值或树页损坏没有 F98 层稳定 corruption 结果；
6. 大 Catalog 没有 split 后 reference-model 证据。

RED 必须因上述 API/行为缺失失败，不能因 fixture、编译环境或随机调度失败。

## GREEN 边界

- 新增 Catalog Lookup Index 的键/值 codec、快照原子发布和六种精确 lookup；
- 复用 B+ Tree、Durable Tree Runtime、Page Manager 和 WAL，不另建存储协议；
- point lookup 不提供全量扫描 fallback；
- 不修改旧 Catalog 对象正文格式，不切 MSQL Executor，不实现 MVCC snapshot。

## 完成门

- targeted 测试重复运行；
- corruption、reopen/crash recovery、reference model、冲突原子性证据；
- 受影响 package 与全仓 `-race`；
- `go vet ./...`、format、全量 CI；
- 更新本门为 PASS，独立 commit 后 fast-forward 合入本地 `main`。

## 完成证据

- RED：`go test ./internal/store/catalogindex` 仅因 Locator、Index、Open 和六种
  lookup API 缺失而失败；
- `go test -count=20 ./internal/store/catalogindex ./internal/store/treecommit ./internal/store/btree`
  通过；
- `go test -race ./...` 与 `go vet ./...` 通过；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build
  全部通过；
- 覆盖 ID/name/alias/scope、not-found、零 WAL 幂等、WAL 前冲突、split reference
  model、crash-before-flush recovery、reopen 与合法外层 Page 中的树损坏。
