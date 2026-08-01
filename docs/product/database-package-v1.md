# Database Package v1

状态：F44 已冻结并实现。

## 格式

扩展名建议使用 `.memora-db`；识别依据始终是内容版本
`memora.database-package/v1`，不依赖文件名。包是一个严格 JSON envelope；F137 起允许顶层
携带可选且严格验证的 [Ed25519 签名](./database-package-signature-v1.md)：

```json
{"version":"memora.database-package/v1","manifest":{},"snapshot":{}}
```

manifest 固定声明 Database ID、名称、用途、scope/anti-scope、Schema version、
作者声明、逻辑 snapshot 版本、对象计数和 SHA-256。相同逻辑 Database snapshot 与
相同作者声明必须产生相同包字节和哈希；包不写入打包时间等非确定性字段。

authority 只包含一个 Database 的 Catalog、当前 Row、完整语义 revision 历史和库内关系。
其他 Database、跨库关系、Agent/机械索引、Router generation、缓存、密钥、代码和 hook
不得进入包。`derived_indexes_included` 在 v1 恒为 `false`；安装后按需重建。

## 三条 MSQL 语句

```sql
PACK DATABASE work BY :author;
OPEN PACKAGE :package READ ONLY;
INSTALL PACKAGE :package TRUSTED;
```

包内容作为 TEXT 参数绑定，不插入 MSQL 源码。CLI 是这三条语句的文件 I/O 薄封装：

```text
memora pack work --by Alice --output /absolute/exports/work.memora-db
memora open work.memora-db
memora install work.memora-db --trusted
```

`pack` 只接受绝对规范化输出路径，拒绝符号链接逃逸，并原子写入权限为 `0600` 的新文件且不覆盖已有路径；`open` 只校验并返回 manifest，不修改 Instance；`install` 必须有显式 `TRUSTED`/`--trusted`，CLI 同时生成绑定 package SHA-256 的 approval，再由 daemon 写入。

## 校验与冲突

打开和安装都先完成严格 envelope、版本、单库边界、manifest/authority 一致性、对象计数和
snapshot SHA-256 校验。未知 envelope 字段、损坏内容、不支持版本均以稳定
`validation_error` 拒绝。包总大小上限为 64 MiB；manifest 文本必须是有效 UTF-8、单行且在字段预算内。

安装是一个原子 Store transaction。目标已有相同 Database ID、大小写不敏感的同名库、
相同 Table ID 或 Relation ID 时返回 `already_exists`，不会覆盖或隐式 merge。
未信任包仍可只读打开；安装返回 Database ID、名称、package hash 和 snapshot hash 收据。

## v1 边界

- v1 不支持包升级、rename、fork 或三方 merge；冲突只拒绝。
- v1 不携带跨库关系，避免安装包隐式取得其他库权限。
- v1 不执行包中文本；文本只保持普通数据库数据权限。
- 商业授权和远程仓库不属于 F44；F137 已补充可验证签名，来源信任仍由本地 Policy 决定。

## 关联

- [可安装的独立语义数据库](./installable-database-package.md)
- [Logical Snapshot v1](../storage/logical-snapshot-v1.md)
- [MSQL](../query/msql.md)
