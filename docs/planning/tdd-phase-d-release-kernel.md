# Phase D：原生极简底座、迁移与产品对账

目标：先让 Memora 拥有自己的最小持久化格式，再接现有逻辑层、迁出 SQLite，
之后完成 Table 级语义树产品旅程。顺序由
[ADR-0003](../decisions/0003-native-minimal-store-first.md)确定。

## 已有发行能力 F44–F50

Database Package、Wiki 导出、安全门、macOS 制品、GitHub Release、格式升级和
干净机器验收已有实现。它们的机械测试可保留，但涉及 SQLite 路径、旧 Router
或旧查询旅程的 fixture 必须随原生迁移更新。

## F51 AI-native 发布门（结论已撤销）

历史实现包含字符向量/cosine，且没有验证 AI 逐层 SQL 导航。代码和报告只作
待清理审计材料，不能授权后续产品发布。

## F52 原生 Header、Frame 与 typed field

目标故事：`US-ENGINE`、`US-RECOVER`、`US-DEVELOPER`。

先测：固定 golden bytes、endianness、CRC、未知版本、非法长度、每字节截断、
Unicode、确定性编码和 fuzz decode 不 panic/不超额分配。

开发：实现 `.memora` File Header、Transaction Frame Header、typed logical field
codec 和稳定错误；本 Feature 不接业务模块、不引入 Page/B+ Tree。

提交：`feat(F52): define minimal native file format`

## F53 追加式事务文件

目标故事：`US-ENGINE`、`US-RECOVER`。

先测：BEGIN/Record/COMMIT、单 writer、多 reader、fsync、reopen、无 COMMIT 尾部、
partial frame、已提交区损坏、排他锁、只读打开和故障注入。

开发：实现 append、commit、scan/recover 和有界 current-offset map。事务只有完整
COMMIT 才可见；不单独实现 Redo/Undo/MVCC。

提交：`feat(F53): persist native transaction frames`

## F54 原生逻辑记录仓库

目标故事：`US-INSERT`、`US-UPDATE`、`US-DELETE`、`US-SPLIT`、`US-ENGINE`。

先测：Catalog、Row revision、History、Relation、Table Route node/membership、
tombstone、expected revision、引用完整性和 reopen 后等价。

开发：把稳定逻辑对象编码为 typed record；维护 stable ID → offset、Route child、
membership 和反向 membership 内存目录。禁止 generic SQLite schema 泄漏进格式。

提交：`feat(F54): store native logical records`

## F55 接通现有 MSQL 与服务层

目标故事：`US-COLD`、`US-READ`、`US-INSERT`、`US-UPDATE`、`US-DELETE`。

先测：同一组 Catalog/CRUD/History/Relation/Route contract 分别跑旧迁移来源与
原生仓库；MSQL 创建、重启、按 RowID 查询、修订和删除的最终 logical snapshot
相同。

开发：增加原生 repository adapter，逐步让业务层从 generic bucket `Store` 迁到
typed repository；MSQL、Policy 和 Result Envelope 不因后端改变。

提交：`feat(F55): run Memora services on native store`

## F56 SQLite → 原生迁移与默认切换

目标故事：`US-RECOVER`、`US-HUMAN`、`US-ENGINE`。

先测：真实 `prototype.sqlite`/`security.sqlite` fixture 导出、空原生目标导入、
canonical hash、双重回读、中断恢复、重复执行和失败保留原文件。

开发：提供只读迁移计划、备份、临时原生文件、校验后原子切换；新 Instance 默认
创建 `.memora`，旧 SQLite 仅触发显式迁移。

提交：`feat(F56): migrate prototype stores to native files`

## F57 删除 SQLite 与旧物理命名

目标故事：`US-DEVELOPER`、`US-HUMAN`。

先测：仓库不导入 SQLite driver，不生成 `.sqlite`/SQLite WAL，不含默认
`prototype.sqlite`/`security.sqlite` 路径；原生安装、升级、doctor 和 package
旅程全绿。

开发：删除 `internal/store/sqlite`、`modernc.org/sqlite`、SQLite benchmark
执行器、耦合 fixture 和已被替换的文档；迁移工具若仍需读取旧文件，隔离为独立
可删除的兼容制品，不进入主 binary。

提交：`refactor(F57): retire SQLite prototype storage`

## F58 Table 级语义树接通

目标故事：`US-COLD`、`US-READ`、`US-DBA`、`US-SPLIT`、`US-OPTIMIZE`。

先测：每 Table 独立 root、逐层 MSQL、RowID leaf、split/merge 上层变更、旧
Database Router 迁移/拒绝和无 MATCH/Vector fallback。

开发：把当前 Database Router 与 Skill Query 改为产品宪章规定的 Table 级主路径，
底层记录直接使用 F54 的 Route node/membership。

提交：`feat(F58): navigate table semantic trees`

## F59 新 AI-native 产品门

按全部相关 `US-*` 重做真实宿主旅程，固定每一步 MSQL、Route Frame、最终原生
文件状态和用户输出。禁止 Vector/cosine 评测或全库 prompt 扫描。

提交：`test(F59): enforce AI-native user stories`

## F60+ 证据驱动的物理优化

只有 F52–F59 的容量、启动、随机读取、并发或空间数据证明需要时，才分别规划
checkpoint、compaction、Page/Buffer Pool、B+ Tree、MVCC、Undo/Redo 和 Binlog。
每项单独过产品门，不能整体照搬传统数据库路线。

## Phase D 退出条件

- 新 Instance 只产生 Memora 自有持久化文件；
- 旧 SQLite 可安全迁移且主程序不再依赖 SQLite；
- MSQL CRUD、History、Relation、Table Router、重启和损坏拒绝运行于原生底座；
- AI 用户故事与物理正确性两套门都通过。
