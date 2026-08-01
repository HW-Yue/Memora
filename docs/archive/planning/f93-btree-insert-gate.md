# F93 B+ Tree Insert 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：未满 leaf/internal Node 可有序插入或替换一个 key；
- 依赖：F90 Node Codec 已完成；
- 契约：见 [B+ Tree Single-Node Upsert v1](../../storage/btree-single-node-upsert-v1.md)；
- 明确不做：树下降、split、parent/root、持久化；
- 用户执行授权：2026-07-31，用户要求执行到 F161；
- 开工前结论：PASS。

## RED 与完成门

- empty/front/middle/back insert 与 duplicate replace；
- leaf/internal metadata 保持，输入与结果深复制；
- invalid key/value/child/kind/header 原子拒绝；
- exact fit 与 over-capacity `ErrNoSpace`，失败后输入 byte-identical；
- 固定 seed 随机操作逐步对拍排序 map reference model；
- 每步 F90 Encode/Decode round-trip；
- targeted `-count=20`、全量、race、vet、完整 CI；
- 不包含 F94 Split；完成后 PASS，否则 INCOMPLETE。

RED 已确认：首次 targeted 测试只因 `B+ Tree single-Node Insert is not implemented`
失败。

完成证据：leaf front/middle/back 与 replace、internal insert/replace、metadata/深复制、
invalid 原子拒绝、exact-fit/over-capacity byte-identical 失败均 PASS；固定 seed 9301
的 500 步操作逐步对拍排序 map 并通过 F90 round-trip；`-count=20`、全量
test/race/vet 与完整 CI 全部 PASS；未包含 F94。完成门结论：PASS。
