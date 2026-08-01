# F100 Row Version Index 开工与完成门

状态：PASS（2026-08-01）。

## 产品审查

用户结果：AS OF REVISION 直接定位目标 immutable Row；AS OF COMMIT_SEQUENCE 用一次
有界树游标定位 floor，不再调用 IDs(Row) 后解码全部 Row record。

依赖：F90–F99 已完成。F100 不做 F101 Table Cursor、F102 Executor 切换、F103
snapshot visibility，也不改变 History provenance 或 Row 正文格式。

## RED matrix

1. 当前 ReadAsOfCommit 枚举全部 Row record，ReadRevision 仍依赖旧 Record 目录；
2. revision key、倒序 commit floor key 和 immutable identity guard 不存在；
3. sequence 0 历史缺口无法与正常 commit 时间线明确区分；
4. immutable 冲突、同批重复和 Row identity 漂移没有 WAL 前原子拒绝；
5. same-commit 多 revision 的 floor 选择无确定性证据；
6. split reference model、并发、crash/reopen、corruption 证据缺失。

## GREEN 边界

- 新增 Row Version key/Locator codec、Append、ByRevision、ByCommit、AsOfCommit；
- 复用 B+ Tree 与 Durable Tree Runtime；
- 不保存 values/provenance，不修改旧 Repository，不接 MSQL；
- 不实现 HistoryPage 范围 API、current Row 更新或 snapshot pin。

## 完成门

- RED 仅因目标 API/行为缺失；
- targeted 重复、reference model、same-commit、legacy 0、并发、reopen、corruption；
- F99/F98/F97d3 回归、全仓 test/race/vet；
- scripts/ci.sh 全门通过；
- 更新为 PASS，独立 commit 后 fast-forward 合入本地 main。

## 完成证据

- RED：targeted test 仅因 Locator、Index、Append、ByRevision、ByCommit 和
  AsOfCommit 缺失失败；
- 20 次重复通过 F100、F99、F98、F97d3 与 B+ Tree 回归；
- 覆盖 exact revision/commit、floor 边界、same-commit highest revision、
  legacy sequence 0、immutable/identity/batch 冲突与零 WAL 幂等；
- 1200 revisions 的 reference model 触发 internal root split，并对多组 target
  sequence 验证 floor 等价；
- 32 个并发同 revision 仅 1 个提交，31 个在 WAL 前冲突；
- crash-before-flush reopen、Locator corpus、合法外层 Page 中树损坏通过；
- 全仓 test、race、vet 与 scripts/ci.sh 全部通过。
