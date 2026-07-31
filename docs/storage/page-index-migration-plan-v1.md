# Page Index Migration Plan v1

状态：F105 Reader 与 F106 apply 均已完成并验收；F107 authority 切换尚未开始。

## 唯一结果

离线 Reader 只读枚举 legacy `database.memora` 的全部 committed Record，并为 F98–F100
生成确定、可校验的 Catalog/current Row/version locator 计划。F105 不创建 Page、
不写 WAL、不发布 root，也不切换 authority；这些属于 F106/F107。

“legacy”指依靠 `File.IDs()` 与逻辑全量扫描决定当前对象的路径，不是 SQLite。
现有 `.memora` immutable Record 继续作为 Row/Catalog body store；迁移后 B+ Tree 决定
哪些 identity/revision 可见，body 不能绕过索引重新成为 fallback authority。

## Source Inventory

`File.Records()` 按 `object_kind + record_id` 返回 committed Record ref、record schema 与
payload length，不包含 transaction BEGIN/COMMIT 或 crash tail。Reader 对每项读取并
校验 payload CRC，按 collision-free length-prefix 流计算 SHA-256。

- 支持并计数 Database、Table、Column、Row、History、Relation、Route、Membership、
  SnapshotMeta、Configuration；
- Opaque、reserved 或未来未知 kind 直接 `unsupported`，不能只迁认识的部分；
- inventory → decode/plan → inventory 再读一次；指纹或计数变化返回 `source_changed`；
- 运行前必须停止 legacy writer；双读用于检测顺序变化，不把非线程安全旧 File 伪装
  成在线 snapshot。

## Plan

`memora.page-index-migration-plan/v1` 包含：

- source SHA-256 与每个已知 kind 的 Record count；
- 完整 current Catalog（保留逻辑 order、name、alias 与 Schema revision）；
- 每个 Row 的 current locator；
- 每个 Row revision 的 immutable version locator；
- canonical plan SHA-256。

Row revisions 按 RowID/revision 排序并必须从 1 连续；Database/Table identity 永久不变；
sequence 0 只能位于首个 positive sequence 前，positive sequence 对同 Row 不得倒退
（同一事务内多次 revision 可以共享 sequence）。
current 必须等于最高 revision。Plan `Validate` 会重算结构与 digest，防止 F106 接受被
调用方修改的计划；`Reader.VerifySource` 必须紧邻 apply 调用，再次核对完整 source
fingerprint 与各 kind count，旧 Plan 不能在源变化后写入目标。

## 失败与边界

- Catalog 悬空引用、Row body/record ID 不一致、revision gap、identity drift、commit
  倒退、CRC 或 codec 损坏均返回 corruption，且没有部分 Plan；
- context cancellation、source change、unsupported kind 使用可区分错误；
- Reader 不读取 SQLite、不创建备份、不删除源文件，也不迁移或重建 Route 语义；
- Route/Relation/History 等虽不生成 F98–F100 locator，仍进入 source fingerprint/count，
  F106 校验期间任何变化都会使计划失效。

## 关联

- [Catalog Lookup Index v1](./catalog-lookup-index-v1.md)
- [Current Row Index v1](./current-row-index-v1.md)
- [Row Version Index v1](./row-version-index-v1.md)
- [SQLite 兼容迁移边界](./sqlite-compatibility-migration.md)
