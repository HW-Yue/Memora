# F140 Package Revocation 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

Instance 可持久记录经 L2/hash-bound approval 的 package 或 signer 撤销；命中撤销的候选仍可
只读 Open 取证，但不能 Install 或作为 Upgrade 目标。

## RED

- revocation 精确绑定 package SHA-256 和/或 signer key ID、actor、reason 与自身 hash。
- 无 L2/approval、损坏记录、ID 重用不同内容均零写入拒绝。
- package hash 命中阻止安装/升级；signer 命中阻止其所有已签名候选。
- restart 后仍生效；同一记录幂等重放不改变结果。
- 已安装库不被撤销动作删除或自动降级，读取保持可用。

## 完成证据

package/signer 命中、幂等、Open 取证与 durable registry race 通过；全仓 CI 全绿。下一项 F141。
