# F108 COW Generation Replacement 开工与完成门

状态：已完成，完成门 PASS。

## 唯一主要结果

构建、验证并原子切换一个完整 Page generation；失败始终保留旧 authority。

## RED

1. marker 只能指向固定 `page-index-v1`，无法表达 replacement identity；
2. rebuild 必须原地修改当前 Tree，失败会污染 reader root；
3. source 在 build 中变化仍可发布；
4. generation 已 rename、marker 未发布时 retry 不能收敛；
5. marker outcome-unknown 后当前进程继续服务或 reopen 猜错 generation；
6. reader 在 build 或 swap 中看到三树混合状态。

最小 RED 证据（2026-08-01）：

```text
go test ./internal/pagestoremigration \
  -run '^TestReplacementMarkerAcceptsStrictEpochGenerationIdentity$' -count=1
FAIL: replacement marker validation error = Page index generation is corrupt: authority marker binding
```

fixture 使用合法 Plan/source digest 和严格 epoch 目录；失败只因为 F107 marker 尚不能
表达 replacement identity。

## GREEN

- compatible marker epoch 与严格 generation name codec；
- `ReplaceGeneration` 的 staging/build/verify/reverify/publish/swap 协议；
- 旧 reader 保持可用，成功只进行一次短 publication swap；
- fault、reference、reopen、idempotence、race 证据全绿。

## 不做

- 删除旧 generation、空间压缩与 free-page reuse；
- 后台自动触发策略；
- change log（F109）。

## 完成门

- [x] marker epoch golden/compatibility/corruption；
- [x] 完整 generation reference model；
- [x] 每个持久化相位 fault matrix；
- [x] reader/build/swap concurrency 与 race；
- [x] targeted、full、vet、race、CI 全绿；
- [x] 独立 commit；合入动作在本文件随 commit fast-forward 到 `main` 时完成。

完成证据：`TestReplacement*` 覆盖成功/重开、marker corruption、32 Current/48 Version
reference model、Tree 与 I/O fault matrix、source change、orphan retry、outcome-unknown
reopen 以及并发 epoch；`scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、
e2e 和 cross-build 全部通过。完成后结论：PASS。
