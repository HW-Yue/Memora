# GitHub Release 自动化 v1

状态：F150 已更新；发布触发、可信签名、权限与制品集合已冻结。

当前已发布：[`v0.1.0`](https://github.com/HW-Yue/Memora/releases/tag/v0.1.0)，目标 commit
为 `bc15346c1cac40d8715ef52a09d7b9a75e889e35`。该版本的本地 publication 已通过签名、
checksum、arm64 smoke 和 clean-machine acceptance；本次 GitHub macOS runner 在解析
Actions 依赖时返回 `Service Unavailable`，未进入项目测试步骤，因此使用同一份本地已验收
publication 完成上传。后续版本仍按下述自动化门禁发布。

## 唯一发布触发

Release workflow 只监听 tag push，不监听 `pull_request`、branch push 或手工
dispatch。GitHub 的 tag filter 只是粗筛，执行时还必须同时满足：

- event 为 `push`，ref type 为 `tag`；
- tag 精确匹配稳定版本 `vMAJOR.MINOR.PATCH`，数字无多余前导零；
- tag 是 annotated tag，tag object 指向 commit；
- GitHub Git Tags API 返回 `verification.verified=true`；
- API tag 名、checkout commit 与 tag target commit 完全一致；
- target commit 已在 `origin/main` 历史中；
- 同名 GitHub Release 尚不存在。

任一条件失败即停止，不能自动创建、移动、替换或删除 tag。

## 权限边界

Workflow 顶层只有 `contents: read`。测试、构建和双架构 smoke job 都保持
只读；只有依赖所有门禁成功的最终 publish job 获得 `contents: write`。
普通 PR CI 继续使用独立 workflow，不能包含 Release 创建或产品制品上传能力。

## 发布制品

一次发布固定上传七个普通文件：

```text
memora_<version>_darwin_arm64.tar.gz
memora_<version>_darwin_amd64.tar.gz
release.json
release.sig
checksums.txt
memora_<version>_skill_bundle.tar.gz
memora_<version>_skill_bundle.tar.gz.sha256
```

前四项沿用 [macOS Release 制品 v1（历史）](../archive/design/macos-release-artifacts-v1.md)。Skill
bundle 从同一 commit 快照确定性生成，包含 canonical Skill、Codex/Claude
adapter、README、PolyForm Noncommercial 正文和商业授权说明；只接受有界普通
文件、稳定路径、固定 mode/timestamp 和无额外 entry 的归档。

Publication builder 必须从 GitHub encrypted secret 获得 Ed25519 私钥与稳定 key ID，
只在权限为 `0600` 的 runner 临时文件中使用私钥并在构建结束删除。Publication
verifier 在上传前及每个下载后的 runner 上，通过独立 public-key secret 建立显式
trust root，重新检查 detached signature、固定文件集合、两份 checksum、release
manifest、Mach-O CPU、Skill bundle 布局与版本。缺失、额外、损坏、错误 signer 或
checksum 不一致均阻断发布；公钥与签名随 workflow artifact 传播，但私钥绝不传播。

## 流水线

1. ARM macOS runner 验证 tag/API 状态并运行完整 `scripts/ci.sh`。
2. 从 tag commit 快照构建 publication，上传为只读 workflow artifact。
3. `macos-15` arm64 与 `macos-15-intel` amd64 分别重新验证并运行 release smoke。
4. 两个原生 runner 另行从 Skill 开始完成零到首条记忆验收，上传通过报告和诊断包。
5. 独立只读 gate 下载 publication 与双架构验收报告，全部验证后才允许进入
   获得写权限的 publish job。
6. publish job 再次完成可信签名验证，生成 GitHub Release notes，创建 draft 并上传七项；核对 draft
   的 tag、target、asset 名称和非零大小后才转为正式 Release。

若新建 draft 的上传或核对失败，workflow 只删除本次创建的 draft，绝不清理
tag；已存在的 Release 在创建前即被拒绝，不会被覆盖。

F150 的 Ed25519 签名只证明 Memora publication 完整性与发布者身份，不等同于
Apple Developer ID 签名、notarization 或通用 provenance attestation。仓库管理员
仍需配置三个 release signer secret，并显式创建和推送签名 annotated tag。

## 关联

- [macOS Release 制品 v1（历史）](../archive/design/macos-release-artifacts-v1.md)
- [干净机器验收 v1](./clean-machine-acceptance-v1.md)
- [测试约定](./testing.md)
- [历史 Phase D](../archive/planning/tdd-phase-d-release-kernel.md)
