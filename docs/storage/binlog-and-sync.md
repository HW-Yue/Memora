# Binlog 与多设备同步基础

状态：确认从第一版持久化协议纳入 Row-based 逻辑 Binlog；事件编码、保留策略和同步冲突协议待设计。

## 定位

Memora 面向个人使用，但一个人的数据库可能分布在电脑、手机和其他设备。Instance 必须提供可连续订阅的 Binlog，为后续增量同步、时间点恢复和变更订阅保留基础。

Binlog 与 Redo Log 不能混用：

- Redo Log 记录物理恢复所需信息，保证本机崩溃后恢复已提交事务；
- Binlog 记录已经提交的逻辑变更，供其他设备或工具理解和重放；
- Undo Log 负责事务回滚和 MVCC 旧版本读取。

## 事务边界

Binlog 只发布已提交事务，并保留完整事务边界和 Instance 内提交顺序。事务回滚时不得留下可同步的业务事件。

Redo 与 Binlog 的一致性参考 MySQL 内部两阶段提交：事务先进入 Redo prepare，随后写入并持久化 Binlog，再完成 Redo commit。恢复时根据 prepare 状态和 Binlog 是否存在决定提交或回滚，避免“本地提交成功但永久缺失同步事件”或“同步端看见本地未提交事务”。

多个并发提交通过 Group Commit 合并 Redo/Binlog 的刷盘成本，并在保持 commit sequence 与 Binlog 顺序一致的前提下批量确认。该协议只协调同一 Instance 内的两个日志，不是跨设备分布式两阶段提交。

## Row-based 事件

Binlog 参考 MySQL Row-based 思路，记录事务提交造成的逻辑 Row 变化，不记录原始 MSQL 供其他设备重新执行。远端不重新运行表达式、时间函数、查询计划或 Agent 决策。

事件使用稳定逻辑 ID 和 Schema version 表达 insert、update、delete 及必要的 Schema 变化。具体采用 before/after image、字段差异还是按操作类型混合编码，由空间、冲突检测和演化测试决定。

## 事件最低信息

候选事件至少携带：

- Binlog format version；
- 本地 transaction ID 和 commit sequence；
- 独立的 global transaction ID，编码 origin Instance/device ID 与原始提交序号；
- Database、Table 和 Row 的稳定逻辑 ID；
- Schema version 与操作类型；
- 可确定性重放的逻辑变更；
- event ID、checksum 和时间信息。

物理 Page、offset、Buffer Pool 内容、模型密钥、Query Workspace 和 Agent 原始上下文不得进入 Binlog。

## 多设备边界

Binlog 是同步的变更来源，不等于完整同步系统。后续同步层仍需负责：

- 设备身份、授权、加密和传输；
- 断点续传、确认位点和日志保留；
- event ID 幂等去重与环路抑制；
- 并发修改、删除传播和 Schema 冲突；
- 新设备先安装快照、再追赶 Binlog；
- 长期离线设备超过保留窗口后的重新同步。

远端事件写入本地时会产生接收端自己的本地 transaction ID 和 commit sequence，但必须保留原始 global transaction ID，不能把同一事务伪装成新的来源并在设备之间无限回环。

## 保留与恢复

Binlog 是有期限的增量记录，不代替完整备份。保留时间或容量必须可配置并可审计；清理前要考虑已注册设备的确认位点。时间点恢复采用“受信快照 + 后续 Binlog”组合。

## 尚未确认

- Row event 采用 before/after image、字段差异还是混合编码；
- 全局 transaction/event ID 的具体编码；
- Group Commit 的批次形成、刷盘策略和失败注入细节；
- 单向复制、中心汇聚还是多主同步；
- 冲突检测与自动/人工解决规则；
- Binlog 分段、索引、压缩、加密和保留配置。

## 关联

- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
- [可安装的独立语义数据库](../product/installable-database-package.md)
- [Instance、Database 与 Table](./instance-database-table.md)
