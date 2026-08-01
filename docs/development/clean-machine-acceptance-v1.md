# 干净机器验收 v1

状态：F50 已实现；双架构零到首条记忆旅程和发布阻断条件已冻结。

## 目标

GitHub Release 在正式发布前，必须分别在原生 macOS arm64 与 amd64 runner
证明全新用户只拿到 Release publication 和其中的 Canonical Skill 后，可以完成：

```text
校验 publication
→ 解包 Canonical Skill
→ 明确授权后通过 HTTPS 下载匹配架构 Release
→ init + daemon + doctor
→ 用 MSQL 保存项目摘要
→ 停止并重启 daemon
→ 查询得到相同摘要
→ doctor 验证 1 Database / 1 Row
```

验收不调用真实模型，也不声称评价开放式总结质量；固定项目摘要只验证 Skill
安装、MSQL 写入、持久化、重启读取和诊断链路没有断裂。AI 效果进入 F51。

## 隔离与下载

每次运行创建独立临时 HOME、TMPDIR、binary 目录和 datadir，不读取真实用户
配置、Instance 或模型凭据。验收器只向安装脚本提供系统工具 PATH、固定版本、
显式 `--yes` 和本次临时路径。

publication 先经过完整 verifier。验收器从已校验 Skill bundle 解出 Canonical
Skill，并用仅监听 loopback 的临时 TLS server 提供同一 publication 的 archive
与 checksum；curl 只信任本次临时 CA。这样在 draft 尚未发布时仍走生产安装脚本
的 HTTPS、checksum、archive layout、binary version、init、daemon 和 doctor
路径，不允许源码回退。

## 报告与诊断包

每个架构固定输出：

```text
report.json
install-diagnostics.tar.gz
```

`memora.clean-machine-acceptance/v1` 报告绑定 product version、commit、darwin
架构、开始/完成时间、八个有序步骤和诊断包 SHA-256。只有完整八步均为
`passed` 且 exit code 为 0 才是通过报告。

诊断包只含 `memora.install-diagnostics/v1` JSON。每步输出限制为 8 KiB，临时
绝对路径与本地 server 地址会被替换，不记录环境变量、用户数据或模型凭据。
失败 job 仍尝试上传报告和诊断包，便于定位；它本身保持失败状态。

## 发布阻断

Release workflow 在两个原生 runner 上并行验收。独立只读 release gate 同时依赖
smoke 和 acceptance，重新下载 arm64/amd64 报告，校验固定文件集、publication
commit、架构、完整步骤、诊断包 hash 及逐步输出 hash。只有该 gate 通过后，
publish job 才获得并使用发布权限；任一报告缺失、失败、损坏、来自其他
commit/版本/架构都会阻断 Release。

本地原生复现：

```bash
./scripts/clean-machine-acceptance.sh \
  /absolute/publication 0.2.0 arm64 /absolute/new-output
```

## 关联

- [Skill 首次安全安装 v1](../agent/safe-bootstrap-v1.md)
- [GitHub Release 自动化 v1](./github-release-automation-v1.md)
- [历史 Phase D](../archive/planning/tdd-phase-d-release-kernel.md)
