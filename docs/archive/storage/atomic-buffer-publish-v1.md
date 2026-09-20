# Atomic Buffer Publish v1

状态：F97d2 已实现并验收，PASS；依赖 F89、F97d1。

## 唯一结果

Buffer Pool 可把一组已经 WAL-durable、已带 record LSN 的 Page after-image 原子发布到
committed Frame；新 Page 可直接安装，Tree control 必须最后。

冻结接口：

```text
Pool.PublishBatch([]BatchChange{Page, ExpectedLSN, New}) → error
```

## Validation 与原子性

- writable Pool、非空 batch，最后一项必须是唯一 `tree-control` Page；
- 其余 Page 只允许 B+ Tree internal/leaf/free；
- 全部 Page 同 space、同 physical generation、identity 唯一、LSN 非零；
- WAL durable LSN 必须覆盖 batch 中最大的 Page LSN；
- existing Page 必须 resident、非 loading，当前 LSN 精确等于 expected LSN，新 LSN
  严格更大；
- new Page 必须 expected LSN 为零且尚不 resident；
- 新 Frame 安装后仍不超过硬容量；只可预选并淘汰不在 batch 内的 clean、unpinned
  victim；
- 任一校验、durability 或容量错误不改变 Frame 值、dirty FIFO、LRU 或 eviction 计数。

成功后 existing/new Frame 都持有输入的深复制并为 dirty；首次变 dirty 时按 batch 顺序
进入 flush FIFO，control 最后。调用期间 Fetch/Inspect/Modify/Flush 不能观察部分 batch。

## 明确不做

F97d2 不创建 WAL transaction、不把 record payload 转成 Page、不管理 Tree root 状态、
不处理 outcome unknown/reopen，也不提供跨多次 Page Read 的 snapshot guard；这些属于
F97d3/F103。
