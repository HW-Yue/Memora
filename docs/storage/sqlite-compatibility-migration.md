# SQLite 兼容迁移边界

状态：F69 已确认并实现。

Memora 主模块、daemon 和 release binary 不再链接 SQLite。只存在旧
`prototype.sqlite` 而没有 `database.memora` 的实例会得到明确
`legacy migration required`，不会静默回退到旧后端。

历史数据读取器隔离在 `compat/sqlite-migrator/` 的独立 Go module。它的流程固定为：

1. 只读打开旧 authority 并导出 Logical Snapshot；
2. 保留 `prototype.sqlite.pre-native.bak`；
3. 导入同目录临时 `.memora` 文件；
4. 回读并比较 canonical snapshot hash；
5. 只有校验相同才原子发布 `database.memora`。

运行方式见 `compat/sqlite-migrator/README.md`。该工具不得被 daemon import，也不
允许双写或把旧文件作为查询 fallback。迁移后原文件和备份由用户显式保留或归档，
Memora 不自动删除。

## 关联

- [Logical Snapshot v1](./logical-snapshot-v1.md)
- [原生极简存储格式](./native-minimal-store.md)
- [F62–F72 历史实现计划](../archive/planning/native-features-transition-review.md)
