# F96 B+ Tree Rebalance 开工与完成门

状态：已完成，完成门 PASS。

## 产品门

- 唯一结果：删除造成的相邻 child pair 可 merge 或 byte-balanced redistribute，并同步 parent；
- 依赖：F90–F95 已完成；
- 契约：见 [B+ Tree Rebalance v1](../storage/btree-rebalance-v1.md)；
- 明确不做：underflow 触发策略、tree descent、Page 回收、root 发布、Buffer Pool/WAL；
- 用户执行授权：2026-07-31，用户要求执行到 F161；
- 开工前结论：PASS。

## RED 与完成门

- leaf merge：顺序、parent child removal、next link、removed Page ID；
- leaf redistribution：variable-size 字节平衡、新 separator、两侧 round-trip；
- internal merge/redistribution：parent boundary、pivot promotion 与完整 child mapping；
- root shrink：唯一 child handoff，非空/leaf/wrong identity 拒绝；
- wrong space/generation/type/level/child identity/index/key boundary/leaf link 原子拒绝；
- merge/redistribute 输出及输入深复制，每个结果通过 F90 Encode/Decode；
- 固定 seed reference corpus 重建 leaf/internal key 与 child 序列；
- targeted `-count=20`、全量、race、vet、完整 CI；
- 不包含 F97 Durable Root；完成后 PASS，否则 INCOMPLETE。

## 完成证据

- RED：leaf merge 首先稳定返回 `ErrRebalanceNotRequired`；随后 leaf redistribution、
  root shrink 与 internal merge/redistribution 分别先红；
- GREEN：能装入单页时 deterministic merge，否则按实际编码字节平衡 redistribute；
  parent separator/child removal、leaf next 与 internal pivot mapping 同步更新；
- 边界：space/generation/type/level/child identity/index/key boundary/leaf link、root
  shape/identity 原子拒绝，merge/redistribute 深复制且每个输出通过 F90 round-trip；
- reference model：固定 seed `9601`/`9602` 各 100 组 variable-size corpus，分别重建
  完整 leaf key/value 与 internal key/child 序列；
- 验收：targeted `-count=20`、package race 和 `./scripts/ci.sh` 全部 PASS；
- 未覆盖项：underflow 触发、tree descent、Page 回收、root 发布、Buffer Pool/WAL，
  继续归属 F97。
