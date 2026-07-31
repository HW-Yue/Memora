# WAL Recovery Open v1

状态：F97b2 候选，依赖 F97b1；待用户批准，未冻结、未实现。

## 唯一结果

用 durable frontier 打开 Segment Set：frontier 以内严格验证，之后的 active crash tail
持久移除，重复重开收敛并恢复单 writer 可写状态。

## 验证与修复

1. 校验目录项、retained manifest、Segment Header 连续性与两个 frontier slot；
2. 严格扫描到 frontier，验证 Record CRC/LSN、transaction count/digest 与 checkpoint；
3. 文件短于 frontier、frontier 内任一损坏或跨 Segment identity 错误均返回 corruption，
   零修复；
4. frontier 所在 Segment 超长则截到精确 offset 并 Sync；
5. frontier 之后的 Segment 从最高 ID 向下删除，最后 Sync 目录；
6. 重新严格扫描并建立 transaction ID、checkpoint 与 writer 状态后才返回。

frontier 后字节不作为已提交证据：完整 commit、partial header/payload、CRC 错误和完整
uncommitted changes 一律丢弃。未知目录项、缺号和 frontier 前损坏绝不通过截尾掩盖。

修复每一步必须可重入：truncate、file Sync、remove 或 directory Sync 失败只返回
recovery-required；下一次 open 从同一 frontier 继续，不能推进 authority。

## RED 与完成门候选

- active tail 位于 change header/payload、commit header/payload 和 frontier publish 前后；
- frontier 前 bit flip/truncate/digest/LSN 损坏拒绝且文件 byte-for-byte 不变；
- frontier 后完整 commit 也被丢弃，已确认 prefix 和 transaction ID 集合保持；
- truncate、file Sync、remove、directory Sync 每点 fault injection 与重复 reopen；
- 跨 Segment 空 active、未确认新 Segment、retained checkpoint 与 reclaim 后布局；
- repair 后 Commit/Roll/Checkpoint 可写，subprocess crash matrix、targeted/full race 和 CI。

## 明确不做

frontier 发布、root/allocator payload、Page redo、Buffer Pool、B+ Tree 或业务 Executor。
