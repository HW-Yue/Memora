# MSQL Parser Core v1

状态：F11 已实现；单语句核心 AST 契约已冻结。多语句和事务边界由 F12 扩展。

## 入口与 AST

`parser.Parse(source)` 接收一条 MSQL 语句，可带一个尾部分号，返回版本为 `memora.msql.ast/v1` 的 Document。AST 使用显式 tagged union，不暴露 SQLite 语法树或执行细节。

F11 支持以下 statement kind：

- `SHOW`：`SHOW INSTANCE`、`SHOW DATABASES`、`SHOW TABLES [FROM database]`；
- `DESCRIBE`：`DESCRIBE DATABASE|TABLE name [COMPACT]`；
- `CREATE`：`CREATE DATABASE name`、基础 `CREATE TABLE name (...)`；
- `SELECT`：projection、`FROM`、可选 `WHERE` 和 `LIMIT`；
- `INSERT`：可选 column list 和一个或多个 `VALUES` row；
- `UPDATE`：一个或多个 `SET` assignment 和可选 `WHERE`；
- `DELETE`：`FROM` 和可选 `WHERE`。

Database、Table 和 Column 是否存在、类型是否合法、VALUES 数量是否匹配等属于 Binder/Policy，不由 Parser 判断。

## 标识符与类型

未引用 identifier 使用 Lexer 的 Unicode identifier 规则；MSQL 已知关键字必须引用后才能作为 identifier。反引号和双引号引用状态保留在 AST 中，解码值不含 delimiter。

限定名按点拆为有序 identifier parts。F11 不限制 parts 数量，Binder 决定各语境允许 `column`、`table.column` 或 `database.table.column` 中的哪一种。

基础 Column definition 为：

```ebnf
column_definition = identifier type_ref [ "NOT" "NULL" | "NULL" ] ;
type_ref          = identifier [ "(" integer { "," integer } ")" ] ;
```

逻辑类型集合、约束和字段预算由 F14 冻结。

## 表达式

Pratt Parser 使用以下从低到高的优先级：

1. `OR`
2. `AND`
3. `=`、`!=`、`<>`、`<`、`<=`、`>`、`>=`
4. `+`、`-`
5. `*`、`/`、`%`

一元 `NOT`、`+`、`-` 高于二元运算。Primary 支持限定 identifier、string/number/boolean/NULL literal、参数和括号表达式。语法接受不代表类型有效；例如 `LIMIT` 的值由 Binder 验证。

## 参数

`?` 生成 `positional` parameter，`:name` 生成 `named` parameter。每次出现都按源码顺序获得从 1 开始的 ordinal；相同 named parameter 重复出现仍保留各自 occurrence，Binder 决定绑定复用规则。参数值不进入 AST 和错误文本。

## Source span 与错误

AST 节点保留 Lexer 的 UTF-8 byte span 和 Unicode 行列位置，但稳定 JSON golden 不序列化 span。Parser 错误包含精确 span，并使用：

- `unexpected_token`
- `unexpected_eof`
- `unsupported_statement`

Lexer 错误原样返回，调用层统一映射为 Result Envelope 的 `parse_error`。

## F12 边界

F11 只接受一条 statement 和至多一个尾部分号。分号后仍有 token 时返回 `unexpected_token`；不得静默忽略，也不得用字符串拆分。F12 将在同一 token stream 上增加 statement list、空语句规则和事务边界。

## 关联

- [MSQL 标准语言](./msql.md)
- [MSQL Lexer v0](./msql-lexer.md)
- [MSQL Result Envelope v1](./result-envelope.md)
