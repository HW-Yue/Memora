# MSQL 参数与表达式 v1

状态：F15c 已实现；用于 F15d Query Planner 和后续 Executor。

## 参数绑定

- named parameter 使用 `:name`，同名重复 occurrence 读取同一个值；
- positional parameter 使用 `?`，按 positional occurrence 顺序消费数组；
- AST 的全局 ordinal 只用于内部定位，named occurrence 不占 positional 数组位置；
- 缺失、额外或未知 style 都返回 `validation_error`；
- 参数值永远不拼回 MSQL source，也不重新进入 Lexer/Parser。

因此包含引号、分号、注释或 SQL 关键字的字符串仍只是一个值，不能改变 AST。

## 值与运算

表达式读取 Row 的系统字段或当前 Catalog Column。F15c 支持：

- literal：string、signed 64-bit integer、boolean、NULL；
- unary：`NOT`、`+`、`-`；
- boolean：`AND`、`OR`；
- comparison：`=`、`!=`、`<>`、`<`、`<=`、`>`、`>=`；
- integer arithmetic：`+`、`-`、`*`、`/`、`%`。

算术只接受整数；除零、类型不兼容和无法表示的 numeric literal 返回 `constraint_violation`。有序比较只接受兼容的 integer、string 或 Timestamp。

涉及 NULL 的普通比较在 WHERE 语境不能成为 true；v1 尚未加入 `IS NULL`。表达式不做字符串到数字/布尔的隐式转换。

Planner 在扫描 Row 前按比较另一侧的 Column 类型验证 parameter/literal。因此空 Table 上的 `integer_column = 'text'` 仍返回 `constraint_violation`；Timestamp parameter 的 RFC3339 string 会先规范化为 UTC。

## 字段绑定

Row 表达式中的字段当前必须是不限定的一段 identifier。系统字段为：

```text
row_id, revision, row_state, schema_version
```

业务字段按 Catalog current name 或 alias 绑定到稳定 `column_id`。未知字段即使 Table 为空也必须在计划阶段返回 `validation_error`，不能因没有 Row 而跳过校验。

## 错误边界

Binder/求值错误只使用已注册的 `validation_error`、`constraint_violation`、`unsupported_statement`、取消/超时和 `internal_error`。参数内容、底层 Store 错误和物理路径不得进入错误文本。

## 关联

- [MSQL Parser Core v1](./msql-parser.md)
- [逻辑类型与字段预算 v1](../data/logical-types.md)
- [MSQL Result Envelope v1](./result-envelope.md)
