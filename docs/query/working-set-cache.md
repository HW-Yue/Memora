# 工作集与 LRU 缓存

状态：已确认采用分层 LRU；容量、持久化形式和命令仍待实测。

## 目标

相邻甚至不同聊天经常继续处理同一个项目。Memora 应记住最近访问的 Database、Route、Schema 和记录定位信息，让 Query Agent 优先命中热路径，减少重复的 `SHOW → DESCRIBE → ROUTE → SELECT` 工具往返。

缓存只优化速度，不能改变查询结果，也不能成为事实来源。

## 不是“按库还是按表”二选一

LRU 容器属于当前 Memora Instance 和调用方工作域，缓存项按层级组织：

```text
Instance / client scope
  → Database working set
    → Route / Table / query artifact
```

- Database 层解决 Agent 尚不知道该看哪张表的问题；
- Route 层保存最近的语义路径和候选入口；
- Table 层缓存 Schema、执行计划和常用定位；
- Page、posting 等物理缓存完全留在 Buffer Pool 和引擎内部。

因此 Database 是 Agent 可见工作集的主要分区，Table 是其内部缓存项，不各自建立互不相关的缓存系统。

## 两级生命周期

### Session LRU

绑定当前宿主会话，记录当前 Database、Route 和最近记录引用。容量很小，主题切换后快速淘汰。

### Warm LRU

绑定稳定的 Instance、调用方和 workspace scope，可跨聊天复用最近工作集。新 Query Agent 启动时只获取一个紧凑 Bootstrap，不接收完整历史索引。

不同 workspace、权限域或用户之间不得共享 Warm LRU，避免把私人项目的热路径泄漏到其他上下文。

## 缓存内容

可以缓存：

- 最近 Database 的 ID、短描述和版本；
- Route 句柄、短摘要和 `route_revision`；
- Table Schema 摘要和 `schema_version`；
- 最近访问记录的可读定位、revision 和关系入口；
- 已编译查询计划、B+ Tree Page 和倒排 posting；
- 精确 SQL 的结果定位及其依赖版本。

默认不缓存到 Agent Bootstrap：

- 完整查询结果或大段记录正文；
- 完整 Router、完整 Schema 和工具日志；
- 对话逐字稿；
- 未提交事务或长期 MVCC snapshot；
- 已吸收的原始资料。

## 启动与查询

Query Agent 的首次调用应获得紧凑工作集，例如：

```json
{
  "databases": [
    {"name": "project_memora", "focus": "query/context", "schema": 12, "route_rev": 31}
  ],
  "max_age": "24h",
  "truncated": false
}
```

它可以直接从最可能的热 Route 构造 SQL；未命中时再走标准发现流程。主 Agent 默认不接收该索引，最终仍只看到受预算约束的 Context Pack。

## LRU 与公平性

第一版使用可预测的 LRU，不先引入复杂学习算法：

- Instance 有总容量上限；
- 每个 Database 有子预算，防止单个热点库挤掉全部入口；
- Session 命中更新会话热度，成功查询同时更新 Warm LRU；
- 只读取 Bootstrap 不提升其中所有条目的热度；
- 大对象按实际字节计费，不按条目数假装等价。

后续只有在基准表明扫描污染严重时，才评估 2Q、SLRU 或频率因子。

## 正确性与失效

缓存键至少绑定：

```text
instance_id + client_scope + database_id + artifact_key
```

缓存值携带 `schema_version`、`route_revision`、对象 revision 和 Policy version。任一依赖变化即失效；陈旧项只能触发重新发现，不能返回旧数据冒充当前结果。

缓存可随时删除，不参与 Undo、历史回溯和 Wiki 导出。

## CLI 进程问题

一次性 CLI 进程退出后，纯内存 LRU 会消失。跨聊天复用需要以下一种运行方式：

1. 交互式 `memora` 或 `memora --stdio` 进程保持运行，LRU 留在当前进程内存；
2. 单次 CLI 命令退出时，将热集合保存为可丢弃的 warm-cache 文件，下次启动恢复到内存。

第一阶段不使用后台 daemon 或 socket。这两种 CLI 生命周期对 MSQL 语义应透明。warm-cache 损坏或丢失只降低性能，不能损坏数据库。

## 尚未确认

- 交互 CLI、`--stdio` 和单次命令怎样共享或恢复 warm-cache；
- Bootstrap 的最大条目数、字符数和 TTL；
- client/workspace scope 如何由 Codex、Claude Code 稳定传入；
- 是否缓存 Context Pack，还是只缓存其记录定位；
- 更新后主动失效与版本校验各自负责哪些层。

## 关联

- [上下文生命周期](./context-lifecycle.md)
- [数据库查询 Sub-agent](../agent/database-query-subagent.md)
- [Instance、Database 与 Table](../storage/instance-database-table.md)
