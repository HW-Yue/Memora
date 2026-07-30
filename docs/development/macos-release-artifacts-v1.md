# macOS Release 制品 v1

状态：F47 实现规格；制品与双许可证契约已冻结。

## 目标与平台

v1 只发布 `darwin/arm64` 与 `darwin/amd64` 的无 CGO 单文件 CLI。两份二进制都必须是对应 CPU 的 Mach-O，可输出相同的版本、commit 与构建时间元数据。完整干净用户旅程属于 F50；F47 提供可在对应架构干净 macOS VM 执行的 release smoke。

## 制品集合

新目录中固定生成：

```text
memora_<version>_darwin_arm64.tar.gz
memora_<version>_darwin_amd64.tar.gz
release.json
checksums.txt
```

每个 archive 根目录只包含：

```text
memora       mode 0755
LICENSE      mode 0644
COMMERCIAL-LICENSE.md mode 0644
README.md    mode 0644
```

Builder 拒绝缺失、空、符号链接或超预算的仓库 `LICENSE`、`COMMERCIAL-LICENSE.md` 或 `README.md`，不在构建时下载或生成法律文本。Memora 使用未修改的 PolyForm Noncommercial 1.0.0 正文并追加 Required Notice：个人和其他非商业用途免费；所有商业用途必须事先取得单独的书面付费商业许可证。

## 可追踪与可复现

`release.json` 版本为 `memora.release/v1`，保存 product version、完整 Git object ID、UTC `built_at`、`source_date_epoch`、Go toolchain 和两份 archive 的 OS/arch/size/SHA-256。

版本必须是稳定 SemVer，commit 必须是十六进制 Git object ID。构建脚本从该 commit 的 `git archive` 快照构建，未跟踪文件不会进入二进制。构建时间来自 `SOURCE_DATE_EPOCH`，未指定时使用目标 commit 的时间；同一 source、Go toolchain、version、commit 和 epoch 必须产生逐字节相同的 archive、manifest 与 checksum。

二进制使用：

```text
CGO_ENABLED=0
GOOS=darwin
GOARCH=arm64|amd64
go build -trimpath -buildvcs=false
```

归档固定 entry 顺序、mode、零 UID/GID 和 timestamp。输出目录必须是尚不存在的新目录，Builder 在临时目录完成自校验后原子发布，失败不覆盖旧制品。

## 构建与验证

tracked worktree 干净后：

```bash
SOURCE_DATE_EPOCH=1785399977 ./scripts/release.sh 0.1.0 /absolute/output
```

Builder 会重新读取 `checksums.txt`、严格解析 manifest、拒绝额外或非普通文件，并校验 archive 安全布局、gzip/tar 可复现元数据、Mach-O CPU、size/hash 和嵌入版本。对应架构 macOS VM 再运行：

```bash
./scripts/smoke-release.sh /absolute/output 0.1.0 arm64
```

Smoke 校验 checksum 和布局，然后执行 `version`、`init`、daemon start/ping、`doctor` 与 stop。

## Gatekeeper 边界

F47 制品尚未承诺 Apple Developer ID 签名或 notarization。Installer/smoke 在已验证 checksum 的二进制无法执行时使用 `spctl` 区分 Gatekeeper 拒绝，并指向 System Settings 的 Privacy & Security；不能把拦截误报为版本不匹配，也不能替换已有 binary。签名与 notarization 在发布自动化前另行冻结。

## 关联

- [安全与隐私门 v1](./security-privacy-gate-v1.md)
- [测试约定](./testing.md)
- [Phase D](../planning/tdd-phase-d-release-kernel.md)
