# Database Package Install v2

状态：F138 已实现。

## 安装结果

Install 先执行 F44 authority/hash 校验和 F137 可选签名校验，再在同一 Store 事务中导入单库
authority，并把 Catalog Database 标记为 `read_only=true`。收据包含 package/snapshot hash、
只读状态，以及签名存在时的 `signer_key_id` 与 `signature_verified=true`。

未签名兼容包仍可在显式 `TRUSTED`、L2 Authorization 和 hash-bound approval 下安装；这表示
用户信任本地来源，不伪装成发布者身份。签名包只有验证成功才可到达安装事务。撤销和本地
trusted signer registry 由 F140 冻结。

## 只读强制

MSQL executor 在限定名绑定前后读取 Catalog 的持久化只读位。Row CRUD、RESTORE、Relation、
Route、Schema、reshape 等写入均返回 `permission_denied`；动态 Route/Relation ID 会在解析真实
Database 后复核。无 Authorization 的可信本地 MSQL 也不绕过 package 只读位。

读取、Router 导航、History 和导出保持可用。要修改安装库必须使用 F141 显式 fork，不能清除
Catalog 位或用普通 DDL 变相解锁。
