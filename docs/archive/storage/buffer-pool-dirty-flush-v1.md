# Buffer Pool Dirty Flush v1

状态：F89 已完成，2026-07-31；冻结 committed Page 发布、flush list 与 WAL-before-data 契约。

## 依赖与配置

可写 Pool 必须同时配置：

- `WALDurability`：返回当前 durable WAL LSN；
- `PageWriter`：把完整 Page 写回其 Data/Index File 位置。

二者都不配置时 Pool 为只读；只配置其中一个属于无效配置。Pool 不拥有或关闭这两个
依赖。PageWriter 的文件 `Sync` 仍由 checkpoint durability barrier 统一负责。

## Committed Modify

Handle 的 `Modify(pageLSN, callback)` 在 Frame exclusive latch 下工作：

- `pageLSN` 必须严格大于当前 Page LSN；
- 修改前必须满足 `durable_wal_lsn >= pageLSN`，否则不发布任何内存变化；
- callback 操作深复制 Page；callback error、错误 identity 或非法 Page 值全部丢弃；
- 成功后由 Pool 写入 `Header.LSN = pageLSN`，原子替换 Frame 值并标记 dirty；
- 首次 dirty 时进入 FIFO flush list；重复修改不重复入列。

这不是 uncommitted write set。调用方必须先完成 F84 durable WAL transaction，再把已
提交的私有 Page 变化发布到共享 Buffer Pool。

## Flush

`Flush(key)` pin 住 Frame，并以每 Frame single-flush mutex 串行化重复刷写：

```text
pin Frame
→ block Modify / snapshot current Page
→ read durable WAL LSN
→ require durable WAL LSN >= Page LSN
→ PageWriter.Write(full Page)
→ only on success clear dirty and unlink flush list
→ unpin Frame
```

durability 查询失败、LSN 不足或 PageWriter fault 都保留 dirty Page。PageWriter 收到深
复制值，不能反向修改 Frame。clean Page 的 Flush 是 no-op。

`FlushDirty(limit)` 按首次变 dirty 的 FIFO 顺序做有界批次；单个失败不阻止同批其他
Page，返回 attempted/flushed/remaining report 与 joined error。

## 与 Eviction 的关系

dirty Frame 永远不是 victim。容量已满且只剩 pinned/loading/dirty Frame 时继续返回
`ErrPoolFull`。成功 Flush 后 Frame 重新成为 clean victim。

## 明确不做

- WAL transaction 创建、私有 write set 或业务 commit 接线；
- Data File fsync/checkpoint（F86b barrier 已定义该职责）；
- 自适应 Page Cleaner、后台线程、dirty 比例阈值、FPI 生成；
- flush 合并、邻接 I/O 或双写缓冲。
