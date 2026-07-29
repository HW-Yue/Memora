# Buffer Pool

状态：整体机制确认参考 MySQL/InnoDB；具体容量和运行参数待实现验证。

## 定位

Memora 的持久数据位于 Data File，执行器不能为每次查询重复直接读取文件。daemon 内的 Buffer Pool 在内存中缓存最近访问的 Page，是存储引擎的基础组件，不是 Agent 专用的查询结果缓存。

```text
MSQL Executor
  → Buffer Pool
    → Data File / Index File / Redo Log
```

Agent 上一次查询读取过的 Page 因为最近被访问，通常会自然留在 Buffer Pool；下一次访问相同 Page 时直接命中内存。引擎不承诺为某个 Agent 永久保留，也不把“上次查过”当成语义相关性或排序信号。

所有 Database 共用当前 Instance 的 Buffer Pool，不按逻辑库建立互相隔离的池。并发规模需要时可以像 InnoDB 一样把总池分成多个 Buffer Pool instance，Page 通过稳定哈希分配；分片只降低并发竞争，不代表 Database 边界。

## 缓存单位

第一版以固定大小 Page/Frame 为基本单位，包括：

- 数据 Page；
- B+ Tree 的内部和叶子 Page；
- 倒排索引 posting Page；
- Data Dictionary 和其他系统 Page。

查询结果、模型上下文、Route 推荐顺序和 Agent 回答不进入 Buffer Pool。执行计划可另设 Plan Cache，但不能与 Page 淘汰混用。

## 基本访问流程

1. 执行器用 `space_id + page_id` 请求 Page；
2. Page Table 命中时固定对应 Frame，并更新最近访问状态；
3. 未命中时选择可淘汰 Frame，从文件读取 Page；
4. 使用期间通过 pin/reference count 防止 Frame 被淘汰；
5. 使用完成后解除固定，Frame 继续留在池中；
6. 内存有压力时按 InnoDB 风格的 young/old 分区 LRU 淘汰冷 Page。

新读入 Page 使用 midpoint insertion 进入 old 区，真正再次访问后才提升为 young，避免一次顺序扫描立即挤掉长期热点。分区比例、提升等待时间和扫描保护参数由 benchmark 调整，不写死为永久常量。

## 写入与正确性

- 修改先发生在内存 Page，并将其标记为 dirty；
- dirty Page 刷回文件前必须满足 Redo/WAL 的持久化顺序；
- dirty Page 不能像 clean Page 一样直接丢弃；
- 后台 Page Cleaner 参考 dirty Page 比例、Redo 生成速度和可用 I/O 做自适应刷脏；
- daemon 崩溃后由数据文件和日志恢复，Buffer Pool 本身不是真相源；
- daemon 关闭时可以只保存最近热 Page 的 `space_id + page_id` 列表，重启后后台预热，不持久化 Page 内容副本；
- 热 Page 清单失效或丢失时从空池冷启动，只影响性能。

## 与 Agent 状态的边界

Query Workspace 可以保存一次 Agent Loop 当前需要的 Route Frame、Schema 版本和候选定位，但它不是 Buffer Pool，也不能控制物理 Page 的保留或检索评分。

如果后续增加逻辑结果缓存，必须单独设计依赖版本、权限 scope 和失效协议，不能借 Buffer Pool 名义返回未经重新验证的旧结果。

## 尚未确认

- Buffer Pool 默认与最大内存预算；
- Page 大小和 Frame 元数据布局；
- young/old 比例、提升等待时间和扫描保护参数；
- dirty Page 比例、自适应刷脏阈值和 I/O capacity；
- Buffer Pool 分片的启用门槛和数量；
- 热 Page 清单的保存比例和加载速率；
- 是否以及如何单独实现 Plan Cache。

## 关联

- [Tablespace、Page 与 Record 布局](./tablespace-page-record-layout.md)
- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
- [上下文生命周期](../query/context-lifecycle.md)
