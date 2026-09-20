# B+ Tree Split v1

状态：F94 已完成；冻结 leaf/internal split 与 root grow 的纯 mutation 契约。

## Split 输入

`SplitLeaf`/`SplitInternal` 接受：

- 已可由 F90 Encode 的原 Node 与 left Page Header；
- 一个 pending insert/replace；
- 调用方已分配的 right Page Header。

两个 Header 必须是同 space/generation、同正确 Page type、非零且 Page ID 不同。函数不
分配 Page、不写 Buffer Pool/WAL。pending 后候选若仍可放入单 Page，返回
`ErrSplitNotRequired`；无法形成两个合法 Node 返回 `ErrNoSpace`。

## Leaf

- 在完整有序候选上枚举非空左右切点；
- 只选择左右都能通过 F90 Encode 的切点；
- 以实际 `header + slots + records` 字节差最小为优先，平局取更小切点；
- left 保留原 Page ID，`next = right Page ID`；
- right `next = 原 next`；
- parent separator 是 right 的第一 key。

## Internal

- 候选 pivot key 提升给 parent，不留在任一 child；
- left 保留原 leftmost child 与 pivot 前 entries；
- right leftmost child 是 pivot entry 的 right child，保存 pivot 后 entries；
- 左右至少各保留一个 separator，level 不变；
- 合法切点同样按编码占用差最小、平局取较小 pivot。

## Root Grow

`GrowRoot` 用显式新 root Header、左右 Page ID、child level 与 separator 构造：

```text
level = child_level + 1
leftmost = left_page_id
separator → right_page_id
```

root ID 必须与 children 不同，结果必须通过 F90 Encode。

所有输出和 separator 都深复制；invalid/no-space 时输入不变。F94 不做递归 parent
传播、allocator、Page 发布、WAL、delete 或 rebalance。
