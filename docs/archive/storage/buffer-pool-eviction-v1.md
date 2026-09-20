# Buffer Pool Eviction v1

状态：F88 已完成，2026-07-31；冻结有界 young/old LRU 与 victim 选择契约。

## 配置与硬边界

Pool 创建时必须给出：

- `Capacity`：Page Table 中 Frame（含 loading 占位）的硬上限；
- `OldFrames`：old 队列目标上限，范围为 `1..Capacity`；
- young 目标上限为 `Capacity - OldFrames`。

配置无效时拒绝创建。F88 不在库内选择默认内存预算或比例，调用方必须显式决定，
以后可依据 benchmark 调参而不改变淘汰语义。

## young/old 队列

- 新装载成功的 Frame 插入 old MRU；同一 single-flight cohort 不算第二次访问；
- old Frame 在后续 cache hit 时晋升 young MRU；
- young hit 移到 young MRU；young 超目标时，其 LRU 降级到 old MRU；
- old 超目标时优先从 old LRU 向前选择 victim；
- 顺序扫描因此只反复替换 old 区，已经晋升的热点保留在 young 区；
- `OldFrames == Capacity` 时退化为单队列 LRU，仍保持相同 pin 和容量规则。

## Victim 与并发

只有 `pins == 0` 且不在 loading 的 Frame 可以淘汰。victim 从 old LRU 开始，必要时
再从 young LRU 选择。淘汰与 Page Table 删除在 Pool 锁内完成，随后 Loader 在锁外执行。

若 Page Table 已达 Capacity 且没有合法 victim，`Fetch` 立即返回 `ErrPoolFull`；它不
偷偷超过容量，也不建立不可取消的等待。调用方 Release 后可重试。被淘汰 Page 的下次
Fetch 重新通过 single-flight 装载。

失败的 Loader 结果仍不缓存；若 miss 为它先淘汰了 victim，失败后空出的容量保持为空，
不会恢复可能已经陈旧的旧 Frame。

## 明确不做

- dirty Frame、flush、WAL-before-data（F89）；
- ghost list、频率 sketch、自适应比例、scan hint、预取；
- 内存字节核算、Pool 分片、后台 eviction 或等待队列。
