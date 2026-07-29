# Instance、Database 与 Table

状态：方向性结论，具体 Tablespace 布局仍待原型验证。

## 结论

个人 Memora 默认采用三级逻辑结构：

```text
一个本地 Memora Instance
  → 多个逻辑 Database
    → AI 自主设计的多张 Table
```

Instance 是一个本地运行和存储实例，不等于“必须只有一个文件”。它统一承载事务、MVCC、Redo Log、全局对象标识、关系索引和缓存。

## 为什么保留逻辑 Database

如果所有内容都只是同一 Database 里的 Table：

- 项目、个人信息、读书和技术知识会混在同一个 Data Dictionary 入口；
- AI 发现 Table 时更容易受到无关字段干扰；
- 权限、导出、归档和删除边界不清楚；
- Table 数量增长后，根目录本身会变成上下文负担。

Database 提供语义和治理边界，Table 负责一个边界内部的结构化建模。

## 为什么不默认拆成多个 Instance

每个逻辑 Database 各用一套物理引擎会让以下能力变复杂：

- 跨库关系和全局检索；
- 跨库事务与一致快照；
- 新 Agent 的统一发现；
- Page、倒排索引和缓存共享；
- 整体备份、恢复与 Wiki 导出。

因此第一阶段让多个逻辑 Database 共享一个 Instance 和事务域。以后如有强隔离需求，再允许独立 Instance。

## AI 何时新建 Database

满足任一条件时，AI 可以提出新建 Database：

- 隐私或访问策略明显不同；
- 需要独立导出、归档或删除；
- 有独立生命周期、项目归属或负责人；
- Table 定义与现有 Database 显著不同，继续加表会污染发现过程。

否则优先在现有 Database 中创建或复用 Table。AI 创建前必须读取现有 Database 的短描述，避免同义库重复出现。

## 跨库能力

- 对象使用 Instance 内唯一标识，显示时仍优先暴露可读名称；
- 跨库关系允许存在，但必须通过 Policy 检查；
- MSQL 可以显式限定 `database.table`；
- 同一 Instance 内可实现原子的跨 Database 事务；
- 根目录只返回每个 Database 的短职责描述，不展开所有 Table。

## 尚未确认

- User Tablespace 按 Database、Table 还是用途划分；
- 何时需要把 Database 迁移成独立 Instance；
- 跨库 SQL 的第一版语法范围；
- 跨库关系在 Wiki 导出中的路径和命名规则。

## 关联

- [存储引擎术语](./terminology.md)
- [AI 自主权与约束](../agent/autonomy.md)
- [工作集与 LRU 缓存](../query/working-set-cache.md)
- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
