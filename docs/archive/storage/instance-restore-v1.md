# Instance Restore v1

状态：F144 已实现。

## 输入与目标

Restore 只接受通过 F143 完整 Verify 的 portable backup，以及调用方明确选择的绝对规范 target。
backup 与 target 必须互不包含；target 必须不存在。协议不覆盖、清空、merge 或 rename 任何已有
目录，因此选错路径不会破坏现有 Instance。

## 恢复状态机

1. 重验 backup 严格布局、manifest hash 和全部文件；
2. 在 target 同级创建私有 staging；
3. 只按 manifest 文件集复制并 fsync，目录/文件权限固定为 `0700`/`0600`；
4. 对 staging 重算文件集、size、SHA-256，并读取 `instance.meta`；
5. 原子 rename 到 target，发布后再次读取 metadata；
6. 返回绑定 backup hash 与 Instance ID 的 `memora.instance-restore-receipt/v1`。

取消或 publish 前 fault 会清理 staging，backup 保持不变且 target 不存在。发布后的 target 保留
原 Instance ID、format version 和 page size；是否切换默认路径、启动 daemon 由 F145 决定。
