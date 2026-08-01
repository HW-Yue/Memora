# F143 Instance Backup 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

对已停止写入的完整 Instance 生成可搬迁、私有权限、逐文件校验且发布后自验证的备份目录。

## RED

- manifest 绑定 Instance ID/format/page size、相对路径、size、mode、SHA-256 与自身 hash。
- source/destination 必须绝对、规范化、互不包含；destination 不存在且 staging 原子发布。
- symlink、非 regular 文件、路径逃逸、文件/总大小预算和迁移中 Instance 拒绝。
- read/copy/sync/rename/verify fault 不发布半成品，source 永不修改。
- Verify 对缺失、多余、损坏、权限放宽和不同 Instance metadata 失败。

## 边界

F143 是离线一致性原语；daemon 编排和普通用户 move 旅程属于 F145。

## 完成证据

create/verify、corruption、fault cleanup 与 race 通过；全仓 CI 全绿。下一项 F144。
