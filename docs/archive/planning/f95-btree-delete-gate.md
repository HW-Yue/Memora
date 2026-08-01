# F95 B+ Tree Delete 开工与完成门

状态：已完成，完成门 PASS。

## 产品门

- 唯一结果：从单 leaf 删除精确 key 后该 key 不可见且邻居不受影响；
- 依赖：F90 Node Codec、F91 Point Search、F93 Single-Node Upsert 已完成；
- 契约：见 [B+ Tree Leaf Delete v1](../../storage/btree-leaf-delete-v1.md)；
- 明确不做：internal/parent mutation、fill factor、borrow、merge、root shrink、WAL；
- 用户执行授权：2026-07-31，用户要求执行到 F161；
- 开工前结论：PASS。

## RED 与完成门

- empty/single/front/middle/back 精确删除，remaining entries byte-identical；
- F91 Searcher 对删除结果返回 `ErrNotFound`，相邻 key 仍命中；
- removed value、输出 Node、输入 key/value 完全深复制；
- absent/empty key、wrong kind/header 原子失败且输入 byte-identical；
- next link 与 empty root leaf 保持合法，每步 F90 Encode/Decode；
- 固定 seed 随机 upsert/delete 对拍排序 map reference model；
- targeted `-count=20`、全量、race、vet、完整 CI；
- 不包含 F96 Rebalance；完成后 PASS，否则 INCOMPLETE。

## 完成证据

- RED：front/middle/back 首次稳定返回 `ErrNotFound`；wrong-kind 随后暴露并修复了
  internal Node 被误报 not-found 的边界；
- GREEN：精确二分删除、removed-value 深复制 handoff、next link 与合法空 leaf 保持；
- 边界：empty/single/absent/empty key/wrong kind/header、失败 byte-identical、F91
  删除后不可见与邻居仍命中均 PASS；
- reference model：固定 seed `9501` 的 2,000 步随机 upsert/delete，每步对拍排序
  map 并通过 F90 Encode/Decode；
- 验收：targeted `-count=20`、package race 和 `./scripts/ci.sh` 全部 PASS；
- 未覆盖项：internal/parent mutation、fill factor、borrow/merge、root shrink 与持久化，
  继续归属 F96/F97。
