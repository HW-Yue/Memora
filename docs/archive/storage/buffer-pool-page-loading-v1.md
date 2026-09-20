# Buffer Pool Page Loading v1

状态：F87 已完成，2026-07-31；冻结 Page Table、single-flight、pin 与 latch 契约。

## 身份与装载

- Page 的缓存身份严格是 `space_id + page_id`，二者共同构成 `Key`；
- Pool 通过注入的 `Loader` 读取 Page，不持有或关闭 Page File；
- 同一 Key 同时未命中时只允许一个 Loader 调用，所有调用者等待同一结果；
- 不同 Key 的 Loader 调用不得被 Pool 全局锁串行化；
- Loader 返回的 Page identity 必须与 Key 完全一致，否则作为稳定错误拒绝；
- 只缓存成功且 identity 正确的结果。失败会广播给当前等待者，随后移除占位，
  下一次 Fetch 可以重试。

Pool 在装载边界复制 payload，调用方不能通过 Loader 原始 slice 修改缓存内容。

## Pin Handle

每次成功 `Fetch` 返回一个独立 Handle，并为对应 Frame 增加一次 pin：

- cache hit 仍创建新的 pin；
- `Release` 恰好解除当前 Handle 的一次 pin；
- 重复 `Release` 和 release 后访问返回稳定错误，pin 不得下溢；
- 等待 single-flight 的调用从加入等待开始即保留自己的 pin 意图；
- Frame 在 F87 中不会被淘汰，pin 是 F88 淘汰判断的权威计数。

## Latch 与可见值

每个 Frame 有独立读写 latch。Handle 只通过回调暴露 Page 快照：

- 多个只读回调可以并发；
- exclusive 回调与同 Frame 的 read/exclusive 回调互斥；
- 不同 Frame 的 latch 互不阻塞；
- 回调拿到深复制 Page，修改它不会改变 Frame；
- Handle 的 Release 会等待该 Handle 已开始的回调结束。

F87 不提供 Page mutation，因此也不产生 dirty 状态。F89 将在 WAL 顺序契约下增加
受控修改与 dirty/flush。

## 明确不做

- Frame 容量上限、young/old LRU、淘汰与扫描保护（F88）；
- dirty、page LSN、flush list 与 WAL-before-data（F89）；
- context 取消、预取、后台 I/O、Pool 分片、热页持久化；
- Page File 生命周期管理与业务存储路径接线。
