# MSQL Catalog DDL v1

状态：F13c 已实现；列表读取边界已由 F110 Metadata Read v1 取代。

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
    title TEXT NOT NULL PURPOSE '展示标题',
    body TEXT(1200) PURPOSE '完整语义正文'
  );
```

Parser 允许缺省语义项以保持语法与 AST 分层；Catalog Binder 必须拒绝缺少 Database `purpose/scope`、Table `purpose/row_semantics` 或 Column `type/purpose` 的创建。

Column 类型由 F14 冻结；`TEXT` 使用 1200 字符启动上限，`TEXT(n)` 持久化 Column 自己的正整数上限。完整集合和输入规则见 [逻辑类型与字段预算 v1](../data/logical-types.md)。

增加 Column 使用：

```sql
ALTER TABLE work.notes
  ADD COLUMN status TEXT NULL PURPOSE '当前工作流状态';
```

## 发现

```sql
SHOW DATABASES [CURSOR :cursor] [LIMIT :limit] [COMPACT];
SHOW TABLES FROM work [CURSOR :cursor] [LIMIT :limit] [COMPACT];
SHOW COLUMNS FROM work.notes [CURSOR :cursor] [LIMIT :limit] [COMPACT];
DESCRIBE DATABASE work [COMPACT];
DESCRIBE TABLE work.notes [COMPACT];
DESCRIBE COLUMN work.notes.title [COMPACT];
```

`SHOW` 始终是有界列表：Database 不嵌套 Table，Table 不嵌套 Column，并按
[Metadata Read v1](./metadata-read-v1.md) 返回 list page envelope。`DESCRIBE ... COMPACT`
同样不展开下一层；不带 `COMPACT` 的 `DESCRIBE` 可以返回该对象的完整当前 Schema，
但不能作为 Admin 的分页列表入口。

## Rename

```sql
ALTER DATABASE work RENAME TO projects;
ALTER TABLE projects.notes RENAME TO knowledge;
ALTER TABLE projects.knowledge RENAME COLUMN title TO heading;
```

rename 保持对象 ID，不移动物理身份，并把旧名称加入 alias。当前名称和所有 alias 参与同一冲突检查。

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
