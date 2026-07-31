# F97c4 Tree Revision Separation 开工与完成门

状态：已批准；授权来自 2026-07-31 后续 Feature 持续执行指令。

## 唯一结果

Tree control、metadata redo 与 recovery 明确分离 physical generation 和 publication
revision，连续普通提交保持同一 Page generation。

规格：[Tree Control v2](../storage/tree-control-v2.md)。

## RED Matrix

- 两次路径内提交后，control revision 已前进而未触及 Page generation 仍旧，现有
  Planner 用 control generation 打开会错误报 corruption；
- control v2 golden/bootstrap/round-trip，v1/unknown version、坏 revision/generation、
  identity、reserved、CRC 拒绝；
- root/allocator redo v2 expected/next revision round-trip，overflow/skip 拒绝；
- recovery 按 revision 做 exact/newer/precondition 判断，root physical generation
  不匹配时事务零写入；
- bootstrap、grow/shrink、fault retry、checkpoint/reopen 与旧 F97c3 回归；
- 连续两次 recovery 后用 control physical generation 创建 Mutation Planner，能读取
  未触及 Page。

## 完成门

- codec/recovery targeted `-count=20`；
- treecontrol、wal、btree 受影响包 race；
- 全仓 test/race/vet、format、`git diff --check` 与 `./scripts/ci.sh`；
- 独立原子 commit，完成后才进入 F97d1。
