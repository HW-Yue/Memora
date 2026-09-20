# Tree Commit Preparation v1

状态：F97d1 已实现并验收，PASS；依赖 F97a、F97c4。

## 唯一结果

把一个 committed/ bootstrap Tree Control 状态和一个私有 `btree.MutationPlan` 严格、
确定地转换为 WAL change records。函数是纯转换：不读写 Page、Buffer Pool 或 WAL。

冻结接口：

```text
treecommit.Prepare(control, mutation_plan) → Prepared{Records}
```

## 顺序

1. `MutationPlan.Changes` 按 Page ID 严格递增；
2. allocated Page 使用 `page-init`，已有 Page 使用 `full-page-image`；
3. retired Page 按 ID 递增生成 `free` full-page-image；
4. allocator 发生前进或包含 retired Page 时生成 allocator redo；
5. root redo 始终最后出现，并把 publication revision 严格加一。

所有 Page after-image 的 LSN 必须为零，WAL commit 后由 record LSN 填入。输出与输入
Payload 不 alias；同一输入产生逐字节相同的 records。

## Validation

- control 必须是合法 committed 状态或唯一 bootstrap；
- plan root 必须位于 `[2, next_page_id)`；
- allocated 必须精确覆盖 `[control.next_page_id, plan.next_page_id)`；
- 每个 allocated ID 必须恰有一个 B+ Tree change 且 expected LSN 为零；
- 已有 changed Page 必须低于旧 high-water、expected LSN 非零且不属于 retired；
- changed/free Page 必须同 space、同 physical generation、正确 identity/type、flags/LSN；
- retired 严格递增，位于旧 high-water 内，不含新 root，且不与 allocated/changes 重叠；
- revision/allocator/Page ID overflow、乱序、重复、空计划或任一编码错误均原子失败。

Bootstrap 只允许从 generation 1、revision 0、root 0、next 2 开始；所有 data Page 都必须
由本计划连续分配。普通提交保持 physical generation 不变。

## 明确不做

F97d1 不检查磁盘当前 Page LSN、不提交 WAL、不修改 Buffer Pool、不发布 control、不
处理 outcome unknown 或 reopen。分别属于 F97d2/F97d3。
