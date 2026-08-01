# F138 Package Install v2 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

通过完整性与可选签名验证的 Database Package 原子安装为持久化只读 Database；重启后所有
MSQL Row、Relation、Route 与 Schema 写路径都拒绝修改，读取保持可用。

## RED

- Install receipt 声明 `read_only=true` 及 verified signer（若存在）。
- 安装事务把只读属性与 Database authority 原子发布。
- 安装后及 reopen 后 SELECT 可读，INSERT/DDL/Route/Relation 写返回 `permission_denied`。
- 无 Authorization、L2 或 approval 的既有安装门仍不能绕过。

## 非目标

本项不创建可写副本；显式可写派生属于 F141。升级、撤销分别属于 F139/F140。

## 完成证据

Catalog/native codec、snapshot、package、executor 定向与 race 全绿；全仓 CI 通过。下一项 F139。
