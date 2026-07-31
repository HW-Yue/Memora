# F92 B+ Tree Range Cursor 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：leaf chain 可按边界、limit 升序续读且不重不漏；
- 依赖：F90 Node Codec、F91 Point Search 已完成；
- 契约：见 [B+ Tree Range Cursor v1](../storage/btree-range-cursor-v1.md)；
- 明确不做：reverse、MVCC、mutation、split；
- 用户执行授权：2026-07-31，用户要求执行到 F161；
- 开工前结论：PASS。

## RED 与完成门

- empty/single leaf、start inclusive/end exclusive、separator 等值；
- 多 leaf 按 limit 分批，batch 间不重读 root/旧 leaf；
- value/bound 深复制，Done 后幂等空 batch；
- leaf cycle、跨叶乱序、空后继、错 identity/type、Reader fault poison；
- 初始下降复用 F91 identity/level/depth 不变量；
- targeted `-count=20`、全量、race、vet、完整 CI；
- 不包含 F93 Insert；完成后 PASS，否则 INCOMPLETE。

RED 已确认：首次 targeted 测试只因 `B+ Tree Range Cursor is not implemented` 失败。

完成证据：empty/single/multi-leaf、inclusive/exclusive 边界、limit 分批、root/旧 leaf
不重读、深复制与 Done 幂等均 PASS；cycle、跨叶乱序、空后继、错 identity/type、
Reader fault 与初始 level corruption 均原子 poison；`-count=20`、全量 test/race/vet
及完整 CI 全部 PASS；未包含 F93。完成门结论：PASS。
