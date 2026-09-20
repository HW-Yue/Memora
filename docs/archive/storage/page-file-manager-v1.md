# Page File Manager v1

状态：F82 已完成；冻结单个 space 文件的 Page I/O。

## 文件布局

一个 Manager 只打开一个 `space_id`：

```text
physical slot 0  → space manifest Page
physical slot N  → Page whose page_id = N
offset           → page_id * 16384
```

slot 0 使用 F81 `manifest` Page，Header 中 `space_id` 为文件身份、`page_id = 0`。
Payload 固定为 16 bytes：

```text
magic[8] = "MEMSPC01" | version u16 = 1 | payload_size u16 = 16 | reserved u32 = 0
```

用户 Page 从 ID 1 连续分配。允许覆盖已存在 Page，也允许写入恰好等于
`next_page_id` 的 Page；禁止跳号制造 sparse hole。Page 物理地址不承载 RowID。

## API 与错误

- `Create(path, spaceID)` 使用 `O_EXCL`、`0600` 创建并同步 manifest；
- `Open(path, expectedSpaceID)` 校验文件长度、manifest checksum/type/identity；
- `Read(pageID)` 只读一个 16 KiB Page，并再次校验 Page 的 space/page identity；
- `Write(page)` 只执行完整 `WriteAt`，不隐式 fsync；
- `Sync()` 暴露文件持久化原语，事务顺序由后续 WAL Feature 控制；
- `PageCount()` 返回不含 manifest 的已分配 Page 数；
- `Close()` 后所有操作返回稳定 closed 错误。

EOF 位置返回 not-found；半 Page、checksum 错误、identity 错误和非 Page 整数倍文件
返回 corrupt。page ID 导致 `int64` offset 溢出时返回 invalid。

## F82 不做

- WAL、事务 COMMIT、checkpoint 和 recovery；
- Page allocator/free list、Extent、Tablespace rolling；
- Buffer Pool、latch、dirty state 和后台 flush；
- slotted payload、B+ Tree 与业务 key。

## 验收

- create/write/read/overwrite/sync/close/reopen；
- 文件权限、manifest、错 space、gap、not-found 和 overflow；
- manifest/data corruption、truncated file/short read/short write；
- fake file 的 I/O fault tests 与 `-race`；
- 测试证明普通 Read 只请求目标 Page，不整文件加载。
