# F144 Instance Restore 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

已验证 F143 备份可恢复到调用方明确选择且不存在的目标目录；staging 完整重验后原子发布，
恢复结果保持原 Instance ID/format/page size。

## RED

- Restore 开始前完整 Verify backup，损坏/权限/额外文件不创建目标。
- target 必须绝对、规范化、与 backup disjoint 且不存在；不覆盖现有 Instance。
- 按 manifest 精确复制，无额外文件；staging metadata 与每文件 hash 重验。
- cancel/copy/sync/publish fault 清理 staging，不修改 backup、不发布 target。
- 发布后再次读取 Instance metadata 并返回 hash-bound receipt。

## 完成证据

restore、overwrite refusal、fault cleanup、metadata/hash 重验与 race 通过；全仓 CI 全绿。下一项 F145。
