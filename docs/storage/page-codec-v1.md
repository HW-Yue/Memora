# Page Codec v1

状态：F81 已完成；本规格冻结 Page 的确定性字节边界。

## 固定尺寸与编码

- Page 固定为 16 KiB；
- Header 固定为 64 bytes，整数统一 little-endian；
- Payload 最大为 `16384 - 64` bytes；
- Encode 未使用区域必须写零，保证同一输入产生同一字节；
- Decode 必须复制 Payload，不能让调用方通过输入切片修改结果。

Header v1：

| Offset | Size | Field |
| --- | ---: | --- |
| 0 | 8 | magic `MEMOPG01` |
| 8 | 2 | format version `1` |
| 10 | 2 | header size `64` |
| 12 | 2 | page type |
| 14 | 2 | flags，v1 必须为 `0` |
| 16 | 8 | space ID |
| 24 | 8 | page ID |
| 32 | 8 | generation |
| 40 | 8 | page LSN |
| 48 | 4 | payload length |
| 52 | 4 | reserved，必须为 `0` |
| 56 | 4 | CRC32C checksum |
| 60 | 4 | reserved，必须为 `0` |

checksum 覆盖完整 16 KiB Page，计算时 checksum 字段视为四个零字节。v1 使用
CRC32C Castagnoli。checksum 不承担加密或恶意篡改防护。

## Page Type

v1 冻结这些物理类型：

- `data = 1`
- `btree_internal = 2`
- `btree_leaf = 3`
- `free = 4`
- `manifest = 5`
- `overflow = 6`

未知类型、非零 flags/reserved、错误 magic/header size/payload length/checksum 都不得
被解释为一个空 Page。未知 format version 返回独立的 unsupported-version 错误。

`space_id`、`page_id`、`generation` 和 `page_lsn` 允许为零；它们的分配与状态
不变量属于后续 Page File Manager、WAL 和 B+ Tree Feature。

## F81 不做

- 文件创建、定位、`ReadAt/WriteAt`、fsync 和 reopen；
- slotted-page、Record、Slot Directory 和 overflow chain；
- WAL、checkpoint、Buffer Pool、B+ Tree 节点格式；
- 压缩、加密、Page repair 和迁移。

## 验收

- 固定 golden header/payload；
- 所有字段 round-trip，输入输出无切片别名；
- 长度、类型、flags、reserved、payload 上限和 corruption table tests；
- fuzz seed 对任意字节不 panic；
- checksum 单 bit corruption 必须拒绝。
