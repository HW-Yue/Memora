# WAL Segment Reclaim v1

状态：F86c 已完成，2026-07-31；冻结 checkpoint 覆盖后的旧 Segment 回收协议。

## Retained Manifest

`segments.manifest` 是 96-byte、CRC32C 保护的原子发布文件，保存：

- first retained Segment ID 与 start LSN；
- 最新 durable Checkpoint 的完整 receipt；
- 已回收事务 ID 的 high-water。

manifest 必须与 retained WAL 内的 checkpoint Record 完全一致。重开时允许磁盘仍残留
ID 小于 first retained 的旧 Segment（表示删除阶段中断），但不会再扫描或恢复它们；
retained ID 必须从 first ID 连续到 active。未知文件、manifest 损坏、缺少绑定的
checkpoint 或 retained Segment 缺号仍拒绝打开。

回收后 transaction ID 小于等于 reclaimed high-water 时拒绝提交，避免已删除日志中的
ID 被重用。更大的 ID 仍按 retained Segment 内的精确集合去重。

## 发布与删除顺序

只有 checkpoint Record 所在 Segment 之前的段可回收；checkpoint 所在段与 active
Segment 永不由本次回收删除：

```text
encode manifest
→ write/fsync temporary file
→ atomic rename to segments.manifest
→ fsync WAL directory
→ close and detach covered Segment handles
→ delete old Segment files
→ fsync WAL directory
```

manifest 发布前的任意失败不得删除 Segment。manifest 发布后的删除失败可以返回错误，
但新 authority 已生效；重开会忽略残留旧段，重试回收可继续清理。临时文件从不作为
authority。

F86c 不截断 retained Segment，不删除 checkpoint 所在段，不实现后台保留时长、容量
阈值、归档上传、PITR 或 Buffer Pool 策略。
