# MSQL Lexer v0

状态：F10 已实现；供 F11 Parser 使用的词法契约已冻结。

## 边界

Lexer 只把 UTF-8 MSQL source 转为带位置的 Token，不判断语句结构、对象是否存在或关键字在当前位置是否合法。Parser 必须消费完整 token stream；不得回退到正则或字符串分割来解析语句。

## Token

v0 输出以下类别：

- word：保留输入大小写，支持 Unicode 字母、数字、组合字符和下划线；
- quoted identifier：反引号或双引号包围，成对 delimiter 表示字面 delimiter；
- string：单引号包围，`''` 表示字面单引号；
- number：十进制整数、小数和带有效数字的科学计数法；
- parameter：`?` 或 `:name`，named parameter 名称遵循 identifier 规则；
- 标点：左右圆括号、逗号、点和分号；
- operator：`=`、`!=`、`<>`、`<=`、`>=`、`<`、`>`、`!`、`+`、`-`、`/`、`%`；
- star：`*` 独立分类，供 F11 区分通配符和乘法语境；
- EOF：始终是成功 token stream 的最后一个 token。

`Token.Lexeme` 是原始 source 切片；`Token.Value` 是解码后的字符串、identifier、参数名或 number 文本。word 的 Value 保留原始大小写。`IsKeyword` 只识别 MSQL 已知关键字并忽略 ASCII 大小写，不能把任意相同 word 当关键字。

## Source span

每个 token 使用半开区间 `[Start, End)`：

- `Offset` 是从 0 开始的 UTF-8 byte offset，可直接切原始 source；
- `Line` 和 `Column` 从 1 开始；
- Column 按 Unicode code point 推进，不按 byte 推进；
- EOF 的 Start 与 End 相同，位于输入末尾。

## 忽略内容

Unicode whitespace、`--` 行注释和 `/* ... */` 块注释不产生 token，但必须推进位置。v0 不支持嵌套块注释。

## 稳定错误

Lexer 错误包含 code 和 source span：

- `unexpected_character`；
- `unterminated_string`；
- `unterminated_comment`；
- `unterminated_identifier`；
- `invalid_parameter`。

非法 UTF-8 返回 `unexpected_character`。任意输入都不得 panic；成功结果中的 token span 必须有序且不越过输入 byte 长度。

## 关联

- [MSQL 标准语言](./msql.md)
- [TDD 开发总计划](../planning/tdd-development-plan.md)
