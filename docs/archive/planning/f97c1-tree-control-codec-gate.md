# F97c1 Tree Control Codec 开工与完成门

状态：完成，PASS；由 F97c 规模 Review 拆出。

## 唯一结果

B+ Tree Tablespace 可确定编解码 slot 1 的 versioned Tree control Page，并支持无
committed root 的 generation-0 bootstrap 状态。

## 产品与边界

- 依赖：F81 Page Codec、F82 Page File Manager；
- 规格：[Tree Control v1](../../storage/tree-control-v1.md)；
- 明确不做：WAL payload、recovery、Tree mutation、online commit 和业务 key。

结论：PASS。Codec 与 redo recovery 是独立格式/故障域；原 F97c 因生产代码超过
约 400 行在实现验收前拆为 F97c1/F97c2。

## 完成证据

- Page type、header identity、32-byte golden payload 与 round-trip：PASS；
- committed state 与 generation-0 bootstrap round-trip：PASS；
- wrong type/space/Page/flags/generation/LSN、magic/version/size/reserved、
  root/high-water 全部拒绝；
- 固定 corruption seed `9703` 共 128 次：PASS；
- Page codec、Tree control targeted/full race、全量 test/vet/CI：PASS。

