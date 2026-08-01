# F94 B+ Tree Split 开工与完成门

状态：已完成，完成门 PASS。

## 产品门

- 唯一结果：满 leaf/internal 加入一个 entry 后可拆为合法两页并生成 parent separator；
- 依赖：F90 Node Codec、F93 Single-Node Upsert 已完成；
- 契约：见 [B+ Tree Split v1](../../storage/btree-split-v1.md)；
- 明确不做：allocator、递归 parent 持久化、WAL、delete；
- 用户执行授权：2026-07-31，用户要求执行到 F161；
- 开工前结论：PASS。

## RED 与完成门

- leaf front/middle/back pending insert，next-link 与 separator 正确；
- variable-size entries 按实际字节选择确定性合法切点；
- internal pivot promotion、right leftmost child 与 level 正确；
- root grow 的 level/children/separator 与 identity 拒绝；
- split-not-required、单 entry 过大、无合法双侧、wrong headers 原子失败；
- 输入/输出深复制，每侧 F90 Encode/Decode；
- 固定 seed reference sequence 证明不丢、不重、有序和 child mapping；
- targeted `-count=20`、全量、race、vet、完整 CI；
- 不包含 F95 Delete；完成后 PASS，否则 INCOMPLETE。

## 完成证据

- RED：leaf front/middle/back 在占位实现下稳定返回 `ErrSplitNotRequired`；internal 与
  root grow 同样先红；
- GREEN：leaf/internal 按实际编码字节选择确定性切点，internal 提升 pivot，root grow
  构造高一级父节点；
- 边界：覆盖无需 split、单 entry 过大、无合法双侧、Header/identity 错误、深复制与
  F90 Encode/Decode；
- reference model：固定 seed `9401`/`9402` 分别重建 leaf entries 与 internal
  key/child 序列，证明不丢、不重、有序且 child mapping 不变；
- 验收：targeted `-count=20`、package race 和 `./scripts/ci.sh` 全部 PASS；
- 未覆盖项：allocator、递归 parent 持久化、WAL、delete/rebalance，继续归属后续 Feature。
