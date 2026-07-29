# MSQL 标准语言

状态：协议方向已确认；正式语法尚未冻结。

## 定位

MSQL 是 Memora 面向 Agent 的唯一正式操作语言。它以 SQL 为主体，增加数据库发现和语义路由语句。CLI 只是 MSQL 的传输与执行入口。

内置 Agent Loop 和外部 CLI/SDK 调用必须提交同一种 MSQL Request，并经过同一套 Lexer、Parser、AST、Binder、Policy、事务和执行器。内置 Agent 不得使用绕过 Parser 的私有数据库操作接口。自然语言由 Agent 转换为 MSQL，不属于 MSQL Grammar。

## 标准进入流程

```sql
SHOW INSTANCE;
SHOW DATABASES;
DESCRIBE DATABASE project_memora COMPACT;
SHOW ROUTES FROM DATABASE project_memora AT '/';
OPEN ROUTE FROM DATABASE project_memora AT '/indexing';
DESCRIBE TABLE project_memora.design_topics COMPACT;
SELECT ... LIMIT 5;
```

## 候选语句

- 发现：SHOW INSTANCE/DATABASES/TABLES；
- 描述：DESCRIBE DATABASE/TABLE；
- 路由：SHOW ROUTES、OPEN ROUTE；
- 数据：SELECT、INSERT、UPDATE、DELETE；
- Schema：CREATE/ALTER/DROP；
- 事务：BEGIN、COMMIT、ROLLBACK；
- 历史：SHOW HISTORY、AS OF VERSION；
- 检索：SEARCH() 表函数或 MATCH 语法，尚未确认。

## 强制规则

- 实际数据只能通过 SQL 查询；
- Route 只返回导航元数据；
- 长文本使用参数绑定；
- 查询必须有结果和输出预算；
- 更新应带 expected revision；
- Parser/AST 验证完整 SQL，正则不负责语法正确性；
- 响应使用稳定 JSON envelope 和错误码。

## Skill 内容

Skill 应包含：

- 协议版本与 EBNF；
- 状态机；
- 参数绑定；
- 输出 Schema；
- 错误恢复表；
- 上下文缓存规则；
- 禁止直接读取物理文件、猜 Schema 或强制覆盖冲突。

Skill 不是安全边界，Parser、Policy 和 MVCC 才是。

## 未决问题

- 采用 MySQL 风格 SHOW，还是更多使用 INFORMATION_SCHEMA？
- MSQL 是 SQL 子集加扩展，还是先实现完整兼容方言？
- Router cursor 是否属于语言标准？
- 多语句事务如何通过短生命周期 CLI 执行？
- 自研 Parser 还是基于现有 Go SQL Parser？

## 关联

- [语义路由](./semantic-routing.md)
- [上下文生命周期](./context-lifecycle.md)
