# Durable WAL Frontier v1

状态：F97b1 已完成；物理格式与 outcome-unknown 边界已冻结。

## 唯一结果

为 Segment Set 保存独立于 WAL 尾部的 durable byte boundary。只有 WAL 字节与 frontier
都成功 Sync 后才返回成功；重开不得把 frontier 之后碰巧完整的 Record 当作已确认提交。

## 冻结格式

Set 创建时建立 `durable-frontier-0.ctrl` 与 `durable-frontier-1.ctrl` 两个固定 4 KiB
control 文件，并同步文件与目录。每个文件是一个 slot：

```text
offset 0   magic[8] = "MEMWDF01"
offset 8   version u16 = 1
offset 10  header_size u16 = 64
offset 12  flags/reserved u32 = 0
offset 16  generation u64
offset 24  segment_id u64
offset 32  durable_end_lsn u64
offset 40  last_transaction_id u64
offset 48  reserved[12] = 0
offset 60  crc32c u32
offset 64  reserved[4032] = 0
```

CRC32C 覆盖完整 4096 bytes，checksum 字段按零计算。文件固定 `0600`，长度、保留字节、
magic/version/header size、非零 generation/Segment ID 和 LSN boundary 全部严格校验。

- 初始 slot 0 generation 为 1，指向 Segment 1 Header 之后，transaction ID 为 0；slot 1
  是全零无效槽；
- 更新只覆盖较旧/无效 slot，Sync 成功后 generation 才成为新 authority；
- 重开选择 CRC、版本、identity、LSN 边界均合法的最高 generation；
- 两个 slot 都无效、generation 冲突或 frontier 超出文件时拒绝打开；
- 旧 slot 在新 slot 成功前保持不变，不使用单文件 truncate/rename 猜测 authority。

既有无 frontier 的 Set 返回 unsupported-format，不静默推断，交由格式升级 Feature 或
显式迁移处理。

## 发布顺序

```text
append WAL bytes
→ Sync WAL
→ write inactive frontier slot
→ Sync frontier file
→ success
```

Commit 更新 `last_transaction_id`；Checkpoint/Roll 保留它。三种操作都必须让 frontier
覆盖各自已确认的最后安全字节。任一步骤
在 WAL 写入开始后失败，当前 Set poisoned 并返回 outcome unknown；重开根据最高有效
slot 收敛。F97b1 不截断 WAL，也不应用 redo。

## RED 与完成门候选

- create/reopen golden、双槽 generation、CRC、identity、LSN 与旧格式拒绝；
- WAL write/Sync、slot partial write/short write/Sync 的逐点 fault injection；
- control 发布失败后不返回成功，重复 reopen 只选择完整有效 slot；
- Commit、Checkpoint、Roll 的 frontier 单调前进且不越过实际 WAL；
- subprocess response-loss、固定 fault seed、targeted/full test/race、vet、format 与 CI。
