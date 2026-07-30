# ADR-0005：B+ Tree 是必做的持久化主索引

状态：Accepted，2026-07-31；物理策略已由
[ADR-0006](./0006-mysql-page-buffer-wal-cow.md) 细化，F81 仍未获实现授权。

## 背景

当前原生 Store 只在打开文件时重建通用 `record_id → offset` Map。精确 RowID
读取仍会重组 Catalog、列举全部 Row revision ID，再选择最新版本。

ADR-0004 曾把可重建内存 Catalog/Row Directory 作为最终第一阶段，并把 B+ Tree
推迟到规模数据证明需要。该方向不符合 Memora 自有数据库内核的最终形态：内存
Map 可以加速热点，但不能代替持久化、有序、可扩展的主索引。

## 决策

1. B+ Tree 是 Memora 必做能力，不再以 benchmark 触发是否实现。
2. F81 必须完成最小持久化 B+ Tree，并接通真实 RowID point-get 与 Table cursor。
3. 第一批至少覆盖这些逻辑 key space：
   - 当前 Row：`table_id + row_id → latest visible revision locator`；
   - Row 版本：`row_id + commit_sequence/revision → immutable record locator`；
   - Table 顺序：`table_id + row_id → live/tombstone state`；
   - Catalog 名称/ID 到当前 Schema revision 的确定性定位。
4. 精确 RowID 读取目标是沿 B+ Tree 根到叶定位，复杂度为 `O(log_B N)`；热 Page
   可以由内存页缓存命中，但不能把“平均 O(1) Map”写成持久化索引保证。
5. daemon 重开必须从已提交 root/manifest 打开树，不全量扫描 Row Record 重建主
   索引；内存 Catalog cache、Page cache 或其他 Map 丢失只影响性能。
6. B+ Tree 必须有稳定 Page identity、内部/叶子节点、顺序叶链、split、merge、
   root grow/shrink、checksum、重开和损坏拒绝。
7. Row、History、Route 和 Change Log 的逻辑语义不因物理树改变。语义 Route Tree
   仍由 AI 导航，B+ Tree 只执行明确键和范围的机械定位。
8. F82 的最小 MVCC 与精确对象写锁保持：reader 使用稳定 snapshot/root，writer
   原子发布数据 revision 与对应索引变化。

## 物理策略

F81 使用 16 KiB Page、单实例 Buffer Pool 与 Redo WAL；普通 B+ Tree 更新走
Page/WAL，COW 只用于 rebuild、compaction、snapshot 与 generation/root swap。
具体编码和实现切片见 ADR-0006。

仍需由实现测试确定 key prefix compression、Page fill factor、free-page 管理、
具体 latch 边界和 Data/Index File 的最终拆分。

## 不随之引入

B+ Tree 必做不等于复制完整 InnoDB。gap/next-key lock、复杂 latch、多 Buffer Pool
instance、doublewrite、change buffer、adaptive hash 和后台刷脏体系仍需独立证据
与 Review。

## 结果

- 主键点查、版本定位和 Table 有序遍历拥有真正的数据库物理索引；
- 重启成本不再必然与全部 Row revision 数量线性绑定；
- 后续 Secondary Index、范围查询和物理优化可以复用同一 Page/Tree 基础；
- 内存缓存与持久化真相源的职责明确分离。
