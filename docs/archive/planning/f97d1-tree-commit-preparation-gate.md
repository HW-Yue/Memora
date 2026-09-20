# F97d1 Tree Commit Preparation 开工与完成门

状态：已完成，PASS；授权来自 2026-07-31 后续 Feature 持续执行指令。

## 唯一结果

合法 Mutation Plan 可确定生成 recovery 已能消费的 Page/allocator/root redo，非法输入
零 I/O、零部分输出。

规格：[Tree Commit Preparation v1](../storage/tree-commit-preparation-v1.md)。

## RED Matrix

- 当前没有 `treecommit.Prepare` API；
- bootstrap、单 Page 更新、root grow/shrink、allocator advance、retired free；
- record 顺序、root-last、Page init/FPI 分类、revision 与 payload golden；
- 输入/输出深复制及重复调用确定性；
- change/allocated/retired 乱序、重复、缺口、重叠与 identity/type/generation/LSN；
- root/high-water、revision overflow、空计划、坏 Page payload；
- 准备出的 records 实际提交后可由 recovery 应用并 reopen。

## 完成门

- targeted `-count=20`、treecommit/wal recovery 集成测试与 package race；
- 全仓 test/race/vet、format、`git diff --check` 与 `./scripts/ci.sh`；
- 完成证据与计划状态同步，独立原子 commit 后才进入 F97d2。

## 完成证据

- bootstrap、已有 Page FPI、新 Page init、root grow/shrink、retired free、allocator 与
  root-last 顺序通过；
- plan identity/type/physical generation/LSN、allocated/retired 顺序、缺口、重叠、
  root/high-water 与 revision overflow 均严格拒绝且不返回部分 records；
- 输入/输出深复制、重复准备确定性通过；
- 准备出的 bootstrap records 经真实 WAL commit、recovery、Page Manager close/reopen
  后得到可解码 control 与 B+ Tree root；
- targeted `-count=20`、treecommit/wal race、全仓 test/race/vet、format、
  `git diff --check` 与 `./scripts/ci.sh` 全部 PASS。
