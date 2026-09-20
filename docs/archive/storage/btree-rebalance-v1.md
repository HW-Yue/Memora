# B+ Tree Rebalance v1

状态：F96 已完成；冻结相邻 child pair 的纯 mutation repair 契约。

## 输入与身份

`RebalanceChildren` 接受 parent、separator index、left/right Node 及各自 Page Header。
parent 必须是 internal，两个 child 必须同 kind、同 level，且比 parent 低一级；三页
必须属于同 space/generation，Page ID 非零且互异。separator index 对应的 parent
children 必须精确是 left/right Page ID。

leaf 还要求 `left.next = right Page ID`，且边界满足
`left.last < parent separator <= right.first`；等号可因删除 right 首 key 后的旧下界而
消失。internal 要求 `left.last < parent separator < right.first`。

## 确定性 repair

调用方只在 child 已判定需要 repair 后调用；本函数不冻结 fill factor：

1. 若 `left + parent boundary + right` 的合法合并结果可由 F90 编码，选择 merge；
2. 否则枚举所有合法非空切点，以两侧实际 `node header + slots + records` 字节差最小
   为优先，平局取更小切点，选择 redistribution；
3. leaf redistribution 把 parent separator 更新为新 right 第一 key，保持
   `left.next = right Page ID` 与 right 原 next；
4. internal redistribution 把一个 pivot 提升为新 parent separator，right leftmost
   child 取 pivot 的 right child；两侧各至少保留一个 separator；
5. merge 保留 left Page ID，从 parent 删除对应 separator/right child，right Page ID
   作为 `removed_page_id` handoff 给 F97；leaf 的 left next 改为 right 原 next；
6. 所有输出、separator 与输入完全深复制，任意失败不修改输入。

`ShrinkRoot` 只接受零 separator 的 internal root，返回其唯一 leftmost child Page ID。
非空 root 返回 `ErrRebalanceNotRequired`；leaf root、错误 identity/shape 返回
`ErrInvalid`。F96 不释放 Page、不修改 manifest、不发布 root、不写 Buffer Pool/WAL。

## 边界

F96 不决定何时触发 underflow，也不实现 tree descent、latch、allocator 或 crash
atomicity。F97 把本纯结果接入 Page/Buffer Pool/WAL，并负责 removed Page 与新 root
的 durable 发布。
