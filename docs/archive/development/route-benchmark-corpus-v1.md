# Route Benchmark Corpus v1

状态：F124 已完成；2026-08-01 冻结并绑定 Real Host Task contract。

## 目的

在 Lexical、Vector 或投机预取实现前，先冻结一套可重复的 Table Router 题库，防止根据
预测结果修改问题、aliases 或正确路径。Corpus 只定义逻辑 Fixture 与 ground truth，
不执行模型、不生成 embedding、不评价任何 arm。

版本为 `memora.route-benchmark-corpus/v1`，生成器版本为
`memora.route-benchmark-generator/v1`；冻结制品位于
`benchmarks/route-retrieval-v1.json`。

## 矩阵

Corpus 使用完整的 6×5 组合，共 30 个独立 scenario：

- fanout：4、8、12、16、24、32；
- depth：1、2、3、4、6；
- difficulty：明显分离、相关主题、边界重叠、同义改写、负例；
- language：中文、英文、混合术语，按固定种子平衡轮换；
- topic：Route fallback、WAL reclaim、Package trust、Schema migration、Source review、
  multi-device sync。

每层候选顺序由自带的确定性 shuffle 固定。路径上的 branch 与兄弟 decoy 都是可展开
节点，避免用 `kind` 猜答案；每个 Leaf 只带一个 RowID，干扰项由 sibling Leaf 表达，
最终仍由 ground truth 指定唯一 RowID。负例保留完整树，但期望停止且不读任何 Row。

## Fixture 与不变量

每个 scenario 包含 stable Database/Table/Route/Row ID、Database/Table 语义、完整节点、
root order、Route revision、snapshot digest、问题、预算与 expected result。

- 正例 expected path 必须从 root 连续到 leaf，最终 RowID 属于该 leaf；
- 负例 expected path 为 `[]`、RowID 为空、停止条件为 `no_matching_route`；
- snapshot digest 绑定 Database、Table 和全部 Node；corpus hash 再绑定全部 scenario；
- prompt 不出现 stable ID，节点/Row 不含正文，只含短语义面与 locator；
- 每题最多 4 次 fallback、64 次 tool call、12,000 context characters、600,000 ms，
  停止条件显式冻结；每题可确定转换成 F123 `memora.real-host-task/v1`；
- checked-in JSON 必须逐字对应当前 generator，未知字段、乱序、重复 ID 或 hash 篡改拒绝。

F125 只能消费该冻结制品；若题库确需修正，必须升版本并保留旧报告，不能覆盖 v1。

## 关联

- [Real Host Contract v1](../agent/real-host-contract-v1.md)
- [Route Retrieval Benchmark v3](./route-retrieval-benchmark-v3.md)
