# F106 Page Store Migration 开工与完成门

状态：已完成，PASS。

## 产品门

- 目标故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；升级可预构建新索引，失败
  或断电后仍有完整 legacy authority。
- 标准旅程：F105 Plan → hidden staging → 三树 build/reopen/verify → atomic publish。
- 唯一主要结果：一个 source-bound Page index generation 被完整发布或完全不可见。
- 架构选择：每树独立 WAL，generation 目录 rename 作为跨树发布点。
- 上下文：纯物理迁移，不进入 Route Frame，不调用模型。
- 用户执行授权：F81–F109 持续授权已记录。
- 明确不做：默认 authority 切换、MSQL writer 接线、删除 legacy body、Route/Relation 索引。
- 开工前结论：PASS。

## RED matrix

| Case | 当前缺口 | 期望 |
| --- | --- | --- |
| bootstrap | F105 Plan 不能落三棵空树 | current final/version/Catalog 全部可查 |
| empty/large | empty version/current 无 bootstrap；大批量无迁移入口 | empty 与 reference model 均可重开 |
| atomic publish | 三树依次写会暴露混合状态 | staging 全验后单点发布 |
| source binding | build 期间源可变化 | publish 前重验并拒绝 stale Plan |
| verification | 写完未证明无遗漏 | point/count/high-water/root/content 全匹配 |
| retry | rename/sync outcome unknown | 相同 Plan 幂等收敛，不同 Plan conflict |
| fault/crash | create/commit/flush/manifest/rename/sync 可失败 | 旧 authority 完整且无半发布目录 |
| corruption | manifest/Page/WAL/locator 可被篡改 | reopen 稳定报 corruption |

首个 RED：有效 F105 Plan 当前没有 `Applier.Apply`，不能生成并发布可重开的三树目录。

## 完成门

- empty、single、multiple revision、large reference model 与重复 apply；
- 每棵树 root/state、全部 locator、Catalog alias/name、version high-water；
- source change、Plan mutation、existing conflict、manifest/Page/WAL corruption；
- 每个 orchestration fault point、rename 后 sync outcome unknown 与 reopen retry；
- legacy source hash 不变、targeted repetition、全仓 unit/vet/race/CI；
- 完成后结论：PASS。首个 RED 仅因 `NewApplier`、`Apply` 与 `OpenGeneration` 缺失
  而失败；Current/Version empty/final bootstrap RED 仅因对应 `Bootstrap` 缺失而失败。

## 验收证据

- empty、两 revision 与 1000 Row/1200 revision 的全部 locator reference model 通过；
- 三树 flush/sync/close 后 reopen，root state、Catalog name/alias、current、history 与
  high-water 全部重验；legacy source bytes 不变；
- manifest/Page/WAL/额外目录项损坏、Plan mutation、source change 与不同 Plan conflict
  均稳定拒绝；
- staging、三树、manifest、source-reverify、rename 前故障均无可见 generation 或残留；
- rename 后与父目录 sync 故障返回 outcome unknown，重试幂等收敛；8 个并发 Applier
  只发布一份，其他全部 reuse；
- targeted `-count=5`、相关包及全仓 `-race`、全仓 unit/vet 和 `scripts/ci.sh` 全绿。
