# WAL Record Stream v1

状态：F83 已完成；冻结单个 WAL segment 的物理字节流。

## Segment

Segment Header 固定 64 bytes：

| Offset | Size | Field |
| --- | ---: | --- |
| 0 | 8 | magic `MEMWAL01` |
| 8 | 2 | version `1` |
| 10 | 2 | header size `64` |
| 12 | 4 | flags/reserved `0` |
| 16 | 8 | segment ID |
| 24 | 8 | start LSN |
| 32 | 28 | reserved `0` |
| 60 | 4 | CRC32C |

CRC32C 覆盖完整 Header，checksum 字段按零计算。`Create` 使用 `O_EXCL/0600`，
同步 Header 和父目录。F83 只管理一个 Segment，不实现自动 rolling。

## Record

Record Header 固定 56 bytes，后接 Payload：

| Offset | Size | Field |
| --- | ---: | --- |
| 0 | 4 | magic `MWAL` |
| 4 | 2 | version `1` |
| 6 | 2 | record type |
| 8 | 2 | header size `56` |
| 10 | 2 | flags `0` |
| 12 | 4 | total length |
| 16 | 8 | LSN |
| 24 | 8 | transaction ID |
| 32 | 8 | space ID |
| 40 | 8 | page ID |
| 48 | 4 | payload length |
| 52 | 4 | CRC32C |

LSN 是 Record 首字节在 WAL 全局字节序中的位置；首条 Record LSN 为
`start_lsn + 64`。下一 LSN 为当前 LSN 加 total length，禁止间隙、倒退和重复。
Payload v1 上限 16 MiB。

Type 只冻结物理标签：page-init、page-delta、full-page-image、root、allocator、
commit、checkpoint。F83 不解释事务、Page 或 checkpoint 语义。

## API 语义

- `Append` 完整写入一条 Record，返回分配的 LSN，不隐式 fsync；
- `Scan` 顺序校验 header/length/LSN/type/checksum，返回损坏前的完整 Record；
- 半 Header、半 Payload、错误 CRC 或乱序 LSN 返回 corrupt，不截断或修复；
- `Sync` 成功后 `DurableLSN = NextLSN`；此前 durable offset 不前进；
- reopen 扫描完整 Segment，拒绝损坏尾部并恢复 Next/Durable LSN；
- Close 后操作返回稳定 closed 错误。

## F83 不做

- transaction BEGIN/COMMIT 状态机和“提交成功”；
- recovery、redo apply、checkpoint 行为和 WAL 回收；
- segment rolling、Group Commit、Page flush 或 Change Log。
