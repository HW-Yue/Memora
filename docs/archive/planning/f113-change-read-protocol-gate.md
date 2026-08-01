# F113 Change Read Protocol 开工与完成门

状态：已完成；RED、GREEN、故障矩阵、daemon/reopen 与完整 CI 均通过。

## 唯一主要结果

F109 immutable committed changes 可通过 Page-indexed MSQL timeline 和 transaction entry
page 有界读取，分页保持事务原子性和固定 snapshot。

## RED

- 已确认 Parser/Executor 不认识 SHOW CHANGES/CHANGE；Admin 只能扫描内部 Repository；
- 没有 commit-sequence/transaction Page index，reopen 后无权威 cursor；
- timeline continuation 会混入新 commit，或 Database filter 泄露其他 scope；
- 单 envelope entries 可一次返回 4096 项，没有上下文边界。

## 完成门

- Page index codec、bootstrap/append、split reference、reopen、corruption 与 race；
- staging 首建、suffix reconcile、body/index mismatch 和故障边界；
- timeline/entry cursor tamper、scope、snapshot、新 commit continuation；
- authorization、summary 无 entries、entry 无 Row values；
- real daemon、replacement/reopen、完整 CI；
- 独立提交并快进合入 `main`。

## 明确不做

- retention、replication、PITR、Trace、Admin 页面或正文 diff。

## 完成证据

- 700 locator split/reference、WAL 未刷盘重开、corruption 无扫描回退与 race；
- staging 各阶段故障清理、rename outcome-unknown 重试、symlink/额外文件失败关闭；
- suffix reconcile、Page/body checksum mismatch、Database filter 与固定 high-water；
- timeline/entry cursor 的 tamper、跨 scope、跨 transaction、未知字段与非 canonical；
- scoped authorization 必须显式 `IN DATABASE`，summary 无 entries、entry 无 values；
- 真实 daemon timeline/entry 分页及重启后 transaction lookup；
- `scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、e2e、cross-build 全绿。
