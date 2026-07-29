# Query Workspace 与缓存边界

状态：已确认 Query Workspace 与 Buffer Pool 分离；是否跨会话恢复逻辑状态仍待实测。

## 两类状态不能混淆

Memora daemon 同时承载存储引擎和 Agent Runtime，但它们使用的内存状态性质不同：

- Buffer Pool 缓存 Data File 中的物理 Page，是文件数据库的必需组件；
- Query Workspace 保存当前 Agent Loop 的少量导航和推理状态；
- 未来如需 Query Result Cache，必须作为第三种独立机制设计。

Agent 上一次查询访问过的数据 Page 会因 LRU 的最近访问特性自然留在 Buffer Pool。这里不建立“上次 Agent 查过，所以优先搜索”的语义通道，也不使用热度修改倒排、Router 或关系评分。

Buffer Pool 的完整设计见 [Buffer Pool](../storage/buffer-pool.md)。

## Query Workspace

一次尚未结束的 Agent Loop 可以临时保存：

- 当前 Database 和 Route Frame；
- 已验证的 `schema_version`、`route_revision` 和 Policy version；
- 本轮候选 Row 定位和 cursor；
- 本轮预算、步骤和工具调用状态；
- 可重建的紧凑 checkpoint。

它默认不保存完整查询结果、业务正文、完整 Router、对话逐字稿或长期 MVCC snapshot。

Query Workspace 绑定调用方、权限 scope 和会话。版本变化后必须重新发现；其内容不能直接作为数据库答案。

## 不做隐式查询结果复用

相同 SQL 是否增加逻辑结果缓存以后再讨论。第一版重新经过 Parser、Policy、Planner 和 Executor；底层 Page 命中 Buffer Pool 后仍可避免磁盘 I/O。

这样能保证：

- LRU 热度只影响物理 I/O 性能，不改变检索相关性；
- 更新后的 Row 不会因旧定位或旧结果被直接返回；
- 不同 Agent 或权限域不会因共享结果缓存泄露内容；
- 缓存丢失只导致冷启动，不影响数据库事实。

## 尚未确认

- Query Workspace 在一次请求、一次客户端会话还是一次任务结束时销毁；
- daemon 重启后是否恢复其可丢弃 checkpoint；
- cursor、候选定位和 Context Pack 各自的有效期；
- 是否需要独立 Plan Cache 或 Query Result Cache。

## 关联

- [Buffer Pool](../storage/buffer-pool.md)
- [上下文生命周期](./context-lifecycle.md)
- [内置 Agent Runtime](../agent/embedded-agent-runtime.md)
