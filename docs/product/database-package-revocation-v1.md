# Database Package Revocation v1

状态：F140 已实现。

## 记录与授权

`memora.package-revocation/v1` 至少绑定一个 package SHA-256 或 signer key ID，并包含稳定
revocation ID、actor、reason 与自身 SHA-256。写入需要 L2 Authorization 和 action
`APPLY_PACKAGE_REVOCATION` 的 hash-bound approval。相同 ID/内容可幂等重放；相同 ID 的不同
内容返回 revision conflict。

记录保存在目标 Instance 的独立 durable registry。读取时严格重验 version、字段和 hash；损坏
registry 失败关闭，不能把损坏误当成“没有撤销”。

## 生效点

Open 仍允许验证和展示已撤销包，便于取证。Install 和 Upgrade Plan 在任何写入前检查候选完整
package hash；有签名时还检查 signer key ID。任一命中返回 `permission_denied`，不会部分导入或
替换。

撤销不删除已安装 Database，不中断本地读取，也不自动选择替代版本。是否升级到安全版本仍需
F139 的显式计划与 approval。
