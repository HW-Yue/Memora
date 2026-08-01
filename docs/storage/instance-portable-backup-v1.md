# Instance Portable Backup v1

状态：F143 已实现。

## 产物

备份是权限 `0700` 的目录，顶层严格只有 `backup.json` 和 `snapshot/`。snapshot 保留 Instance
相对目录结构，所有文件规范化为 `0600`、目录为私有权限。manifest 使用
`memora.instance-portable-backup/v1`，记录 Instance ID、format/page size、创建时间、文件数、
总字节及每个文件的规范相对路径、mode、size、SHA-256，并对自身内容计算 hash。

## 创建与验证

source/destination 必须是绝对规范路径、互不包含，destination 必须不存在。创建只读取 source，
在 destination 同级私有 staging 中复制并 fsync 每个文件；staging 完整 Verify 后才原子 rename，
发布后再次 Verify。取消、预算、symlink、非 regular 文件、copy/fault/rename 失败都清理 staging，
不修改 source。

Verify 要求严格顶层布局、无缺失或额外文件、私有权限、逐文件内容一致、manifest 自哈希一致，
并重新读取 snapshot 内 `instance.meta` 验证 Instance ID/format/page size。

## 一致性边界

F143 备份原语要求调用方先停止 daemon/写入，避免跨文件复制时产生逻辑撕裂。F145 的 move
旅程负责 stop → backup → restore/verify → switch → start 编排；本层不会自行杀进程。
