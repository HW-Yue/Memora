# Catalog Lookup Index v1

状态：F98 已完成；格式与行为契约已冻结。

## 唯一结果

Catalog 的稳定 ID、当前名称、历史别名和当前 Schema revision 可通过持久化
B+ Tree 精确定位。一次点查只沿 root-to-leaf 路径读取，不枚举完整 Catalog，且
not-found 或索引损坏时不得静默回退到旧全量扫描。

F98 只建立物理定位层，不把旧原生 Record File 替换为 Page Store 真相源；MSQL
执行器切换属于 F102，唯一新写 authority 切换属于 F107。

## 键空间

所有键使用版本内固定的二进制类型前缀和大端长度前缀，不使用分隔符拼接：

| 前缀 | 键组件 | 目标 |
| --- | --- | --- |
| `database/id` | Database ID | Database locator |
| `database/name` | canonical name/alias | Database locator |
| `table/id` | Table ID | Table locator |
| `table/name` | Database ID + canonical name/alias | Table locator |
| `column/id` | Column ID | Column locator |
| `column/name` | Table ID + canonical name/alias | Column locator |

canonical 与 Catalog v1 一致：去除首尾空白后做 Unicode lowercase。ID 精确匹配，
不做 lowercase。单个 UTF-8 组件最多 2048 bytes；超限是 validation error，不截断。

## Locator v1

值采用严格、确定性的版本化二进制编码，包含：

- object kind；
- stable object ID；
- 所属 Database ID（Table/Column）；
- 所属 Table ID（Column）；
- 当前 Schema revision。

解码必须拒绝未知版本、未知 kind、零 revision、空必填 ID、层级字段不一致、保留位
非零和尾随字节。名称键返回的 locator 还必须与查询 scope 一致，否则视为 corruption。

## 发布与冲突

- 一个快照中的 Database 名称/别名全局唯一；Table 在 Database 内唯一；Column 在
  Table 内唯一；同 kind 的稳定 ID 全局唯一。
- 同一 canonical 键指向不同对象时，整次发布在写 WAL 前失败。
- 快照替换先计算有序 diff，再用 F97d3 在一个 WAL 事务中原子发布树页和 root。
- 初次发布创建 Page 2 空叶根；后续 reopen 只读取 committed control root。
- 相同快照重复发布是无 WAL 的幂等成功。

## 完成证据

- name、alias、ID、Schema revision、not-found 与 scope isolation；
- 冲突在 WAL 前失败，旧快照仍可读；
- 足量对象触发 leaf/internal split，并与 map reference model 一致；
- crash-before-flush 后 reopen 由 WAL 恢复并得到同一结果；
- locator/tree Page corruption 返回稳定错误且无 scan fallback；
- F97d3 targeted、race 与全仓门禁保持通过。
