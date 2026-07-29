# 逻辑类型与字段预算 v1

状态：F14 已实现并冻结。

## 类型集合

MSQL v1 启动类型：

| 声明 | 规范值 | 输入 |
|---|---|---|
| `INTEGER` | signed 64-bit integer | Go 整数或不含小数/指数的 JSON number |
| `BOOLEAN` | boolean | JSON/Go boolean |
| `TIMESTAMP` | UTC timestamp | `time.Time` 或 RFC3339/RFC3339Nano string |
| `TEXT` / `TEXT(n)` | UTF-8 string | string |
| `RELATION_ID` | opaque Row reference | `row_` 开头的 ASCII identifier |

类型名大小写不敏感，Catalog 保存上表中的规范大写名称。F14 不做隐式字符串/数字/布尔转换；例如 `"true"` 不是 `BOOLEAN`，`1.0` 不是 `INTEGER`。

`RELATION_ID` 只验证稳定 ID 外形，不验证目标 Row 是否存在。引用完整性和跨库 Policy 属于 F18。

## NULL

每个 Column 显式保存 nullable。输入 `NULL`：

- nullable Column 返回规范 `NULL`；
- `NOT NULL` Column 返回 `constraint_violation`；
- NULL 不进入其他类型转换。

## 文本预算

`TEXT` 创建时把启动默认值 1200 写入 Column 的 `max_characters`；`TEXT(n)` 使用正整数 `n`。预算按有效 UTF-8 的 Unicode code point 计数，不按 byte 计数。

超过当前 Column 上限返回 `value_too_long`，错误只包含 Column、实际字符数和上限。验证器不返回截断值，也不修改原输入。无效 UTF-8 返回 `constraint_violation`。

1200 是启动配置而不是隐藏常量：每个 Database snapshot 持久化当前 Column 值，后续调整仍必须经过显式 MSQL、revision 和 Policy。

## 规范化

- `INTEGER` 统一为 `int64`，无符号溢出拒绝；
- `TIMESTAMP` 统一为 UTC，无法按 RFC3339Nano 解析的字符串拒绝；
- `TEXT` 保持原字符串，不做 trim、Unicode normalization 或大小写折叠；
- `RELATION_ID` 保持原值，只允许 `row_` 后出现 ASCII 字母、数字、`_`、`-`。

## 稳定错误

- 非法/未知类型声明：`validation_error`；
- NULL、类型不匹配、溢出、无效时间/UTF-8/Relation ID：`constraint_violation`；
- 文本超限：`value_too_long`。

所有错误实现 `StableCode()`，供 Binder 和 F15 Executor 直接映射到 Result Envelope。未知底层错误不能借类型错误通道泄漏。

## 关联

- [语义记录模型](./semantic-records.md)
- [Catalog v1](./catalog-v1.md)
- [MSQL Catalog DDL v1](../query/catalog-ddl.md)
- [AI-native 可演化配置](../product/adaptive-configuration.md)
