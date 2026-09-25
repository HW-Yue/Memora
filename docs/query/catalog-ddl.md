# MSQL Catalog DDL v1

状态：F13c 已实现；F110 冻结列表读取，F112 增加 Column semantic role，F135 增加
跨库 Catalog Atlas。

## 创建

语义元数据项可按任意顺序出现，但每项最多一次：

```sql
CREATE DATABASE work
  PURPOSE '保存项目知识'
  SCOPE '活跃项目'
  ANTI SCOPE '私人日记';

CREATE TABLE work.notes
  PURPOSE '保存耐久笔记'
  ROW SEMANTICS '一行是一条已审阅笔记'
  SCOPE '已确认知识'
  ANTI SCOPE '原始资料'
  (
    title TEXT NOT NULL PURPOSE '文档标题' ROLE title,
    summary TEXT(2500) NOT NULL PURPOSE '完整自足的文档正文' ROLE summary
  );
```

Parser 允许缺省语义项以保持语法与 AST 分层；Catalog Binder 必须拒绝缺少 Database `purpose/scope`、Table `purpose/row_semantics` 或 Column `type/purpose` 的创建。

**行的形状是引擎的（ADR-0014）**：能声明的列只有带 `ROLE title` 与 `ROLE summary` 的两列，各至多
一个；其余列（包括不带 role 的）在 `CREATE TABLE`、`ALTER TABLE ADD COLUMN` 与
`PLAN SCHEMA CHANGE` 的 `ADD_COLUMN` 上都会被拒绝，拒绝信息里带形状规范原文。`ALTER_COLUMN`
（加宽上限）与 `DROP_COLUMN`（退役旧列）不受影响；旧实例已存的列不会被重新校验，照常可读可写。

Column 类型由 F14 冻结；`TEXT` 使用 1200 字符启动上限，`TEXT(n)` 持久化 Column 自己的正整数上限。
**`TEXT(n)` 的 n 是 Unicode 码点（字符）不是字节**——引擎按 `utf8.RuneCountInString` 校验，
超过上限回 `value_too_long`；读侧在 `columns[].max_characters` 里把这个上限带出去，所以只读也能核对。完整集合和输入规则见 [逻辑类型与字段预算 v1](../data/logical-types.md)。

`ROLE` 可选，v1 接受 `title/summary/identity/status/fact/rationale`。title 与 summary 在单个 Table
内各最多一个；未声明 title 时 Row detail 只能回退到 RowID/revision，不能猜列名。

增加 Column 使用：

```sql
ALTER TABLE work.notes
  ADD COLUMN status TEXT NULL PURPOSE '当前工作流状态' ROLE status;
```

## 发现

```sql
SHOW DATABASES [CURSOR :cursor] [LIMIT :limit] [COMPACT];
SHOW TABLES FROM work [CURSOR :cursor] [LIMIT :limit] [COMPACT];
SHOW COLUMNS FROM work.notes [CURSOR :cursor] [LIMIT :limit] [COMPACT];
SHOW CATALOG ATLAS [CURSOR :cursor] [LIMIT :limit] [BYTES :bytes] COMPACT;
DESCRIBE DATABASE work [COMPACT];
DESCRIBE TABLE work.notes [COMPACT];
DESCRIBE COLUMN work.notes.title [COMPACT];
```

`SHOW` 始终是有界列表：Database 不嵌套 Table，Table 不嵌套 Column，并按
[Metadata Read v1](../archive/query/metadata-read-v1.md) 返回 list page envelope。`DESCRIBE ... COMPACT`
同样不展开下一层；不带 `COMPACT` 的 `DESCRIBE` 可以返回该对象的完整当前 Schema，
但不能作为 Admin 的分页列表入口。

Atlas 把授权 Database 与其 Table 的短语义元数据扁平分页，不展开 Column/Route/Row；
同时受 entry 与 rows JSON byte 预算约束。完整契约见 [Catalog Atlas v1](./catalog-atlas-v1.md)。

## Rename

```sql
ALTER DATABASE work RENAME TO projects;
ALTER TABLE projects.notes RENAME TO knowledge;
ALTER TABLE projects.knowledge RENAME COLUMN title TO heading;
```

rename 保持对象 ID，不移动物理身份，并把旧名称加入 alias。当前名称和所有 alias 参与同一冲突检查。

## Amend the description

```sql
ALTER DATABASE projects SET PURPOSE '保存项目知识' SCOPE '活跃项目' ANTI SCOPE '私人日记';
ALTER DATABASE projects SET SCOPE '当前有效的活跃项目';
ALTER DATABASE projects SET ANTI SCOPE '';
```

库的三个描述字段**可修订，不是建库时焊死的**。它们不是装饰：冷启动 agent 每次写入前读它们来决定
这条知识放哪，所以一句 `scope` 写着「当前有效」时，"当前"一变它就变成假话，而且**主动误导放置**。

`SET` **只写它点名的字段**，其余保持原值——语句点名的就是全部指令。`PURPOSE` 与 `SCOPE` 必须非空
（一个装不进任何东西的库不是合法结果）；`ANTI SCOPE ''` 是撤销一条边界声明的写法，不是错误。
每个字段最多出现一次，至少写一个；顺序不限。字面量是单引号字符串（与 `CREATE DATABASE` 相同，
`PURPOSE "x"` 会被当成 quoted identifier 拒绝）。

这是一条**有界的元数据写**：一次一行、一个事务、一条 change 记录，风险等级 L2（任何 `ALTER`
都是 L2）。它与 `ALTER DATABASE … RENAME` 一样**不带 `expected_schema_version` 前置条件**——
本项目的版本前置条件只存在于 Row 与 Table 形状的写入（`PLAN/APPLY SCHEMA CHANGE`），库级 DDL
从来没有，改描述比改名只轻不重。`DESCRIBE DATABASE` / `SHOW DATABASES` 回读三者；`anti_scope`
未设置时不出现在行里（`omitempty`），所以"设了"和"空"在读面上都是"没有"。

## Binder 限定名

- Database 必须是一段：`database`；
- Table 必须是两段：`database.table`；
- Column 必须是三段：`database.table.column`；
- `SHOW TABLES` 必须显式 `FROM database`，F13 不维护隐式 current database；
- quoted identifier 作为一个完整名称段传给 Catalog，Parser 不自行拆解其内容。

Binder 只依赖 Catalog 接口，不读取 Store 或 SQLite。Catalog 的 `validation_error`、`already_exists` 和 `not_found` 原样保留；取消、超时和未知 Store 错误规范化为已注册 Result code，不泄漏物理实现细节。

## 关联

- [Catalog v1](../data/catalog-v1.md)
- [MSQL Parser Core v1](./msql-parser.md)
- [MSQL Result Envelope v1](./result-envelope.md)
