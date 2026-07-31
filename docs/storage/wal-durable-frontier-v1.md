# Durable WAL Frontier v1

状态：F97b1 候选，待用户批准；未冻结、未实现。

## 唯一结果

为 Segment Set 保存独立于 WAL 尾部的 durable byte boundary。只有 WAL 字节与 frontier
都成功 Sync 后才返回成功；重开不得把 frontier 之后碰巧完整的 Record 当作已确认提交。

## 候选格式

Set 创建时建立两个固定 4 KiB control 文件，并同步文件与目录。每个 slot 包含：

```text
magic/version/slot_size
generation
segment_id
durable_end_lsn
last_transaction_id
reserved
crc32c
```

- 初始 slot 指向 Segment 1 Header 之后，transaction ID 为 0；
- 更新只覆盖较旧/无效 slot，Sync 成功后 generation 才成为新 authority；
- 重开选择 CRC、版本、identity、LSN 边界均合法的最高 generation；
- 两个 slot 都无效、generation 冲突或 frontier 超出文件时拒绝打开；
- 旧 slot 在新 slot 成功前保持不变，不使用单文件 truncate/rename 猜测 authority。

精确 offset/golden 在 F97b1 RED 前冻结；既有无 frontier 的 Set 不静默推断，交由格式
升级 Feature 或显式迁移处理。

## 发布顺序

```text
append WAL bytes
→ Sync WAL
→ write inactive frontier slot
→ Sync frontier file
→ success
```

Commit、Checkpoint 与 Roll 都必须让 frontier 覆盖各自已确认的最后安全字节。任一步骤
在 WAL 写入开始后失败，当前 Set poisoned 并返回 outcome unknown；重开根据最高有效
slot 收敛。F97b1 不截断 WAL，也不应用 redo。

## RED 与完成门候选

- create/reopen golden、双槽 generation、CRC、identity、LSN 与旧格式拒绝；
- WAL write/Sync、slot partial write/short write/Sync 的逐点 fault injection；
- control 发布失败后不返回成功，重复 reopen 只选择完整有效 slot；
- Commit、Checkpoint、Roll 的 frontier 单调前进且不越过实际 WAL；
- subprocess response-loss、固定 fault seed、targeted/full test/race、vet、format 与 CI。
