# Index Generation Manifest v1

状态：F25 已冻结派生索引 generation 的验证、发布、pin 与 GC。

边界：本规格只描述历史 F25 的 Router、Agent inverted、mechanical inverted 三类组合
manifest。F124c Route-only vector generation 使用独立的 Instance 派生目录与 active
marker；它不复用本规格的三类组合，也不写入 Database Package。

## 对象

每个 Database 独立维护 Router、Agent inverted、mechanical inverted 三类 generation。generation record 包含 stable generation ID、kind、覆盖 commit sequence、checksum、validated 标记和 staged/active/retired 状态。

首次 manifest 发布必须一次给出三类 generation。manifest 保存：

```text
database_id + manifest_revision
kind → generation_id + covered_commit_sequence + checksum
```

后续发布可只替换一类；未涉及的 active generation 原样保留，不复制或重建。

## 旁路与发布

新 generation 先以 staged 状态登记。构建方完成内容校验后提交 observed checksum；只有与 staged checksum 完全一致的 validated generation 才能发布。

发布要求 expected manifest revision，并在一个 Store transaction 中完成：

1. 验证全部目标 generation；
2. 把被替换 generation 标记 retired；
3. 把目标标记 active；
4. 写入包含完整三类组合的新 manifest revision。

commit 前崩溃/rollback 仍读取旧组合；commit 后重启只读取完整新组合，不存在半切换。

## Query Pin

query 开始时读取一次完整 manifest 并 pin 其中三类 generation。publish 后该 query 仍使用原 manifest snapshot；新 query 使用新 revision。Pin Release 幂等。

同一进程内 publish、pin 与 GC 由 manifest service 串行化，防止“读到旧 manifest、尚未增加引用计数就被 GC”的竞态。

## GC

GC 只回收满足全部条件的 generation：

- state 为 retired；
- 不在当前 active manifest；
- pin count 为零。

staged generation 和 active generation 不由该路径误删。当前原型由 registry GC 删除 generation metadata；原生派生文件接入同一生命周期时，必须在删除 metadata 前回收对应 content。两种实现都不得触碰权威 data/history。

## 关联

- [Database 物理目录](../../storage/database-file-layout.md)
- [物理与检索索引](../../storage/indexing.md)
- [Pending Reindex v1（历史）](./pending-reindex-v1.md)
