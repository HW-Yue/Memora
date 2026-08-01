# Skill 首次安全安装 v1

状态：F39 实现规格，已冻结。

## 授权与入口

Memora binary 不存在或版本过旧时，宿主先向用户说明将下载或编译本地可执行
文件、安装位置和 Instance 位置。只有用户明确同意后，才能从 Canonical Skill
目录运行 `scripts/install.sh --yes`；脚本自身也拒绝缺少 `--yes` 的调用。

v0 只支持 macOS arm64 和 amd64，不使用 sudo，不写系统目录，也不安装模型
Provider 或密钥。默认 binary 位于 `~/.local/bin/memora`，Instance 仍遵守用户级
Application Support 目录约定；宿主可传绝对 `--install-dir` 和 `--data-dir`。

## Release 优先

脚本按 OS/arch 选择固定版本的 `memora_<version>_darwin_<arch>.tar.gz`，通过
HTTPS 下载归档和 `checksums.txt`。只有归档 SHA-256 与 manifest 中该文件的唯一
条目一致、布局只含 `memora`、且 binary 自报版本匹配时，才在目标目录原子替换。

checksum 不匹配、manifest 缺项、归档布局异常或版本不符是安全失败：保留旧
binary，不进入源码回退。正确版本已存在时不重复下载，但仍执行健康检查。

## 源码回退与离线

仅当 Release 或 checksum manifest 无法取得时才允许 Go 回退。有明确
`--source-dir` 时从该仓库构建；否则使用固定 module tag 执行 `go install`。没有
Go 或完全离线且没有本地源码时，停止并给出可恢复诊断，不写入半成品。

## 初始化与验收

binary 就绪后顺序执行幂等 `memora init`、daemon ping/start 和 `memora doctor`。
init 已存在时复用同一 Instance；daemon 已运行时不重复启动。doctor 未返回
healthy 就不能向用户报告安装完成。临时归档和 staged binary 始终清理。

## 关联

- [Canonical Skill v1](./canonical-skill-v1.md)
- [macOS Instance 数据目录](../storage/macos-instance-directory.md)
- [历史 TDD 开发总计划](../archive/planning/tdd-development-plan.md)
