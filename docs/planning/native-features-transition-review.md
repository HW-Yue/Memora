# F62–F72：正确性、迁移与产品主路

状态：已批准；只有 F53a–F61 全部闭环后才开始。

实现进度：F62–F69 已完成；F70–F72 继续按顺序推进。

## F62 Transaction Frame

- 故事：`US-ENGINE`；
- 增加：transaction ID、BEGIN/RECORD/COMMIT 与 transaction digest；
- 测试：多 Record 全部可见、无 COMMIT 全部不可见、显式 rollback 不追加；
- 不做：fsync durability、kill -9、MVCC/Undo；
- 完成：reopen 只发布有完整 COMMIT 的事务。

## F63 Crash Recovery

- 故事：`US-RECOVER`、`US-ENGINE`；依赖 F62；
- 增加：写入顺序、fsync 边界、尾部截断、已提交区损坏拒绝；
- 测试：每个字节截断点、partial header/payload/commit、重复 reopen；
- 不做：Redo/Undo、doublewrite、Group Commit；
- 完成：kill/partial-write 后恢复到最后完整提交。

## F64 跨对象 Mutation

- 故事：`US-UPDATE`、`US-DELETE`、`US-CORRECT`；
- 旅程：Row + History + Relation + Route membership 在同一事务更新；
- 测试：每个故障点 rollback、旧 membership 不可见、History 不缺失；
- 不做：split/merge 语义决策；
- 完成：现有单 Row mutation 不再留下半套结构。

## F65 Split/Merge Mutation

- 故事：`US-SPLIT`、`US-OPTIMIZE`；
- 旅程：读取旧 Row/关系/Route → 新 Row → supersede → 重组上层 Route → 验证；
- 测试：2→1、1→2、多 membership、失败全回滚、历史可追溯；
- 不做：引擎自动决定怎样拆分，决策仍由 AI；
- 完成：产品宪章中的 split/merge 闭环成立。

实现结果：AI 提交显式 reshape plan；引擎在一个事务中发布来源 Row 的
`superseded` revision、新目标 Row、SPLIT/MERGE History、Relation、Route 节点
revision 与 membership revision。引擎只校验形状和引用，不推断怎样拆分或合并。

## F66 Native Snapshot Import/Export

- 故事：`US-RECOVER`、`US-DEVELOPER`；
- 旅程：logical snapshot → native import → reopen → export → canonical hash 相同；
- 测试：未知字段、空目标、重复 ID、悬空引用、失败无部分导入；
- 不做：读取真实用户默认 datadir；
- 完成：SQLite 与原生之间有后端无关的安全桥。

实现结果：新增后端中立的 LogicalDocument API 和 native snapshot bridge。导入先
完整校验 Catalog、Row/History、Relation 引用与连续 revision，再用单一原生事务
发布；空目标、重复导入和悬空引用稳定失败。未发生 authority 修改时，未知顶层及
嵌套字段通过原始 logical source 保持 canonical hash；修改后只输出当前 typed
authority，避免返回陈旧快照。

## F67 SQLite/Native Shadow Parity

- 故事：`US-DEVELOPER`、`US-HUMAN`；
- 同一固定 MSQL corpus 分别跑 SQLite 与 native，比较 envelope 与 snapshot；
- 覆盖：Catalog、CRUD、History、Relation、Router、restart；
- 不做：双写真实用户数据、不比较 SQLite 内部文件；
- 完成：差异为零或每一项偏差已获明确批准。

实现结果：固定 MSQL corpus 对两个后端执行 Catalog、INSERT、UPDATE、History、
Relation 与精确 RowID SELECT，逐条比较 executor envelope，并在双后端 restart 后
复查读路径和 logical snapshot canonical hash。native 现与旧后端使用同一
commit sequence 语义。旧 Database Router 不作为 parity 目标：它已被产品决策
取代，Table Router 的原生重开测试由 F61 覆盖，MSQL 主路由 F70 接入。

## F68 默认切换与显式迁移

- 故事：`US-HUMAN`、`US-RECOVER`；
- 新 Instance 默认创建 `.memora`；旧实例输出只读计划、备份、迁移、回读后切换；
- 测试：中断、重复执行、空间不足、校验失败和原文件保留；
- 完成：daemon 默认运行于 native，但仍可回滚到迁移前备份。

实现结果：新 daemon 的主 authority 固定为 `databases/database.memora`，MSQL
Catalog/Row 直接使用 typed native repositories。F68 验证了旧格式的备份、logical
snapshot 导入、回读 hash 和原子发布；F69 将该 reader 移入独立兼容 module，主
程序遇到 legacy-only instance 会明确拒绝并要求先运行迁移工具。

## F69 删除 SQLite

- 故事：`US-DEVELOPER`、`US-HUMAN`；依赖 F68 稳定；
- 删除：driver、`internal/store/sqlite`、主 binary 文件名、SQLite benchmark 与 fixture 耦合；
- 旧格式 reader 如需保留，隔离成主 binary 外的兼容工具；
- 完成：`go.mod` 和主程序不含 SQLite，新旅程全绿。

实现结果：删除 root driver、`internal/store/sqlite` 和 SQLite FTS runtime；root
`go.mod` 只剩 UUID 与系统调用依赖。仍使用窄 Store/Tx 的辅助服务和旧逻辑测试
统一运行于 append-only `nativekv`，daemon 文件名改为 `.memora`。真实 SQLite
reader 只存在于 `compat/sqlite-migrator` 独立 module，不能被主 binary import。

## F70 AI Table Router 查询

- 故事：`US-COLD`、`US-READ`；
- 旅程：SHOW DATABASES → TABLES → DESCRIBE → ROUTES/UNDER → OPEN → SELECT；
- 测试：真实 Codex/Claude-compatible transcript、Route Frame 预算、回退与权限；
- 不做：MATCH、query_terms、Vector/cosine、全库 prompt 扫描；
- 完成：AI 主查询只走 Table 语义树和 RowID 回表。

## F71 删除旧检索主路

- 删除：Database Router 主路、MATCH fusion、Agent/mechanical 语义评分、Vector benchmark；
- 可保留的精确/字面查询必须有独立名称，不能自动兜底语义导航；
- 测试：仓库扫描、Canonical Skill、CLI/e2e 均无隐藏 fallback；
- 完成：实现与产品宪章不再存在双架构。

## F72 AI-native 用户故事门

- 覆盖：CRUD、冷启动、纠正、Schema、DBA、split/merge、资料吸收和宿主切换；
- 证据：实际 MSQL、每层 Route Frame、最终 Row/Schema/Route、用户回复；
- 禁止：Vector/cosine 基线替代故事验收；
- 完成：所有目标 `US-*` PASS，未通过项不得包装为已发布能力。

## 继续后置

Page、B+ Tree、Buffer Pool、MVCC、Undo/Redo、Binlog、并发写和 compaction 继续
等待 F72 前后的真实性能/正确性数据；届时每项重新 Review，不自动进入路线。
