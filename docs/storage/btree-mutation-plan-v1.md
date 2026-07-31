# B+ Tree Mutation Plan v1

状态：F97a 已完成并冻结；F97b–F97d 不在本规格范围内。

## 唯一结果

把一个已提交 root 上的一组 byte-key upsert/delete 计算为完全私有、确定性的 Page
after-image 计划；失败或放弃计划不会修改 Reader、Buffer Pool、文件或 WAL。

冻结接口：

```text
NewMutationPlanner(space, generation, root, next_page_id, Reader)
planner.Upsert(key, value)
planner.Delete(key)
planner.Plan() → root, next_page_id, PageChange[], allocated[], retired[]
```

`next_page_id` 是第一个未分配 ID。每个 `PageChange` 携带原 Page 的
`expected_lsn`（新 Page 为 0）和 LSN 归零的 after-image，供 F97d 做提交前冲突检查和
WAL 编码。输出按 Page ID 排序，所有 byte slice、Node、Page 和结果互不 alias。

同一 Planner 可承载显式 MSQL transaction 的多次 mutation。每个方法自身原子：后续
mutation 失败时保留此前已成功的私有计划，但不泄漏本次的 Page、allocator 或 root
变化；调用方可丢弃整个 Planner 完成 rollback。

## Upsert

1. 从当前私有 root 下降，最多 64 层；记录 internal Page 和所选 child index；
2. leaf 能容纳时用 F93 insert/replace；
3. leaf 满时从私有 high-water 连续分配 right Page，用 F94 split；
4. separator 向父层传播；父层满时递归 internal split；
5. 原 root split 时再分配新 root，用 F94 grow 并更新计划 root；
6. replacement 因 value 变大也可以触发 split，不保留静默 no-space fallback。

所有读取先查 Planner overlay，再查 Reader；同一 Page 在一个 Planner 内只从 Reader
加载一次。错误 identity、space、generation、level、cycle、separator/child mapping
或超过最大深度都返回 corruption，不能生成部分 mutation。

## Delete 与 underflow

删除先用 F95 精确移除 leaf key。非 root Node 满足以下任一条件时触发 repair：

- 结构为空；或
- F90 编码后的 payload 使用量严格低于 Page payload 容量的 50%。

优先选择右 sibling；没有右 sibling 才选择左 sibling。调用 F96 时：

- 能装入一页则 merge，并把被移除 Page ID 放入 `retired`；
- 否则按 F96 的实际字节平衡结果 redistribute；
- parent 变化继续按同一 underflow 规则向上递归；
- internal root 变为零 separator 时 shrink 到唯一 child，并 retire 旧 root；
- root leaf 允许在删除最后一个 key 后保持空树。

50% 是触发阈值，不是对 variable-size entry 的伪造硬不变量；若无法 merge，F96 的最小
字节差合法 redistribution 即为结果。F97a 不复用 retired ID，也不生成 free Page。

## Plan 不变量

- 新分配 ID 连续且不与 root、已加载或已 staged Page 重叠，overflow 原子失败；
- 可达 Page 全部同 space/generation，层级逐层减一，child/cycle/leaf link 合法；
- leaf key 全局严格递增，internal separator 与 child 边界一致；
- 每个 changed Page 只有一个最终 after-image，`expected_lsn` 始终来自首次读取；
- `allocated`、`retired` 互斥且有序；失败 mutation 恢复其进入前的私有状态；
- 同一 Planner 内先分配后又失去可达性的 Page 仍保留在 `allocated` 和 after-image 中，
  不进入 `retired`；high-water 只前进不回退，避免制造未初始化的 allocator 空洞；
- Planner 不枚举整棵树，只读取 mutation path、必要 sibling 与受影响 leaf link。

## RED 与完成门

- 最小 RED：单 leaf `Upsert` 由可编译占位 API 返回
  `ErrMutationPlanNotImplemented`；
- 多层 leaf/internal split 传播与 root grow；
- replacement 扩容 split、重复 mutation 的 overlay read 与最终 after-image 去重；
- delete 的 right-first/left fallback、merge/redistribute 级联与 root shrink；
- absent delete、no-space/overflow、Reader fault、identity/generation/level/cycle/boundary
  corruption 均保持 Planner 进入本次调用前的状态；
- 固定 seed reference model 交错 upsert/delete，每一步从计划 Page 重建完整排序 map，
  校验 leaf chain、separator、可达/allocated/retired 集合；
- 输入/输出深复制、确定性结果、targeted `-count=20`、package race、全量 test/race、
  vet、format 与 CI。

## 明确不做

WAL、root/control Page 格式、Buffer Pool publish、文件 I/O、Page ID 复用、业务 key
编码、Catalog/Row 接线、MVCC、锁、checkpoint 或 compaction。
