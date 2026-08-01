# Policy Enforcement v2

状态：F136 已实现；取代
[安全与隐私门 v1](../archive/design/security-privacy-gate-v1.md) 中仅有 Database scope 的授权协议。

## Authorization

宿主的结构化 MSQL input 使用 `memora.authorization/v2`：

- `actor`：本次宿主身份；
- `authorized_databases`：最多 32 个精确名称、别名或稳定 ID；
- `default_level`：本次默认上限，规范化宿主输入必须显式发送；
- `database_levels`：可选的逐库上限，key 必须已存在于 scope；
- `approval`：仅供需要 review hash 的动作，不会提升风险等级。

Go 内部调用省略 `default_level` 时兼容为 L1。Skill 和外部客户端不得依赖该兼容值。
名称比较不区分大小写；重复 canonical key、未知等级和 scope 外 override 均被拒绝。

## 引擎等级

- L0：SHOW、DESCRIBE、SELECT、Route 导航、只读计划、Pack/Open 等读取。
- L1：同库 Row CRUD、RESTORE 和同库 Relation 写入。
- L2：Catalog/Schema、reshape、Route 结构、配置、安装及跨库 Relation。
- L3：不可逆清理、降低隐私或强制覆盖；当前无公开 MSQL 入口，默认不可达。

静态限定名在执行前检查；Route ID、Relation ID、别名和稳定 ID 在解析真实对象后再次检查。
跨库 RELATE/UNRELATE 以真实 Database ID 判定。Apply Route/Schema 与 Install 还必须满足
原有 action + SHA-256 绑定 approval，scope、等级和 approval 缺一不可。

## 信任边界

无 Authorization 的内部 Go/本地普通 MSQL 仍是同一 macOS 用户的可信操作员路径；Package
安装等原有强制授权入口不降级。Authorization 和 approval 是结构化用户意图，不是密码学身份。
