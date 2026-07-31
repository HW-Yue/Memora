# Crash Recovery v1

状态：F85 已完成；冻结已提交 Page redo 的幂等重放边界。

## 输入与顺序

Recovery 接收一个已由 F83 校验的 WAL Segment，以及 `space_id → Page Store` 映射。
它先用 F84 transaction scanner 校验 commit boundary、count、first LSN 和 digest，只按
WAL 顺序重放完整已提交事务。完整但未提交的尾部忽略；WAL 截断、CRC/LSN 损坏仍由
F83 拒绝，F85 不静默修复日志本身。

每个事务先完整验证并在内存 staging，确认所有 redo 可解释后才写 Page Store；脏
Page 按 `(space_id, page_id)` 顺序写入，并对该事务触及的每个 Store 执行一次 Sync。
任一 Write/Sync 失败都不报告成功；重启后从头重放即可。

## Redo Payload

`page-init` 与 `full-page-image` Payload 是 F81 编码的完整 16 KiB Page：

- Page identity 必须与 WAL Record 的 space/page 一致；
- 模板的 Page LSN 必须为零，由 recovery 改为 change Record LSN 后重新编码；
- 两者都能覆盖 torn Page；`page-init` 表达首次物理创建，FPI 表达 checkpoint 后的
  首次修改镜像。

`page-delta` 只做固定长度 Payload patch：

```text
magic[4] = "MDEL"
version u16 = 1
header_size u16 = 16
offset u32
length u32
bytes[length]
```

offset/length 必须完全位于当前 Page 的逻辑 Payload 内，不改变 Payload 长度。Page
缺失或 checksum 损坏时 delta 不能猜测旧内容，必须等待更早的 page-init/FPI 修复。

## 幂等与报告

- 磁盘 Page 有效且 `page_lsn >= change_lsn` 时跳过该 change；
- 部分恢复后重跑会跳过已落盘 change，并继续应用更高 LSN；
- FPI/page-init 后的同事务 delta 在 staged Page 上继续；
- Report 只记录已验证事务数、Page writes、跳过的 redo 数和最后 commit LSN；
- Recovery 不发布 reader view，不生成新 WAL，也不修改未提交事务。

F85 不定义 `root`/`allocator` Payload；遇到它们返回稳定 unsupported-redo 错误且
不部分应用所在事务。F85 不实现 checkpoint、WAL 回收、Buffer Pool、后台刷脏、
B+ Tree 或业务 Store 切换。
