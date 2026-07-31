# MSQL Result Envelope v1

状态：F09 已冻结并实现 JSON 类型、校验、golden 和错误码注册表。

## 统一外形

单语句与多语句使用同一顶层结构；即使只有一条语句，也放入 `results[]`：

```json
{
  "version": "memora.result/v1",
  "request_id": "request-7",
  "ok": true,
  "results": [],
  "truncated": false,
  "warnings": []
}
```

`results`、`warnings`、每条结果的 `columns`、`rows` 和 `warnings` 永远是 JSON array，空值编码为 `[]` 而不是 `null`。可选对象和值不存在时省略，不用含义不清的 `null`。

`ok` 只在没有 request error 且每条 statement 都为 `succeeded` 时为 `true`。warning 和正常的有界截断不等于执行失败。

## Statement Result

每条输入语句按原顺序返回：

```json
{
  "index": 0,
  "statement": "SELECT",
  "source": "SELECT row_id, title FROM notes LIMIT 5",
  "status": "succeeded",
  "columns": [],
  "rows": [],
  "affected_rows": 0,
  "truncated": false,
  "warnings": []
}
```

`statement` 是稳定的大写 AST kind，不是 SQLite 或内核名称。`source` 是可定位失败语句的有界原文，参数值不得插值回显。`columns` 描述名称、逻辑 MSQL 类型和 nullable；`rows` 使用 Column 名到 JSON value 的对象。写入可以返回 `affected_rows`、对象 `revision` 和本次事务的 `commit_sequence`。

状态固定为：

- `succeeded`：语句成功，包括显式执行成功的 `ROLLBACK`；
- `failed`：该语句执行失败；
- `skipped`：因所在事务已失败而未执行；
- `rolled_back`：语句曾执行，但所属事务随后自动回滚。

后三种必须携带 statement error，且其中的 `statement_index` 与结果 index 相同。批次里一项失败不允许吞掉其他项的结构化结果。

## Request Error 与 Statement Error

无法形成 statement list 的错误，例如空请求或请求级协议校验失败，放在顶层 `error`，此时 `results` 为空。已经定位到具体语句的 Parser、Binder、Policy、事务或执行错误只放入该 statement result。

错误字段为稳定 `code`、面向人的 `message`、`retryable`、可选 `statement_index` 和有界 `details`。未知 code 拒绝序列化，防止自然语言临时错误成为客户端契约。v1 初始注册：

```text
invalid_request, parse_error, unsupported_statement, validation_error
not_found, already_exists, permission_denied, revision_conflict, write_conflict,
constraint_violation
value_too_long, transaction_aborted, invalid_transaction_state
cancelled, deadline_exceeded, output_truncated, internal_error
```

`internal_error` 不得携带 stack、物理 Page、文件路径或底层 SQLite 信息。新增机器可读语义必须先登记 code 和恢复规则。

`permission_denied` 表示请求语法有效，但调用方无权读取或修改目标逻辑
对象。客户端可以缩小授权范围内的请求或向用户说明受限，不能换用物理文件、
另一条检索通道或更高权限入口绕过。

`write_conflict` 表示另一个活跃 transaction 正持有同一精确逻辑对象，属于可重试
瞬时错误；它不同于已提交版本不匹配的 `revision_conflict`。客户端可在持有者终态后
有界重试，仍须保留 expected revision 校验，不能用重试强制覆盖新 revision。

## Warning 与截断

Warning 也使用注册过的 `code + message + details`。Statement 范围的 warning 放在对应结果；request 范围的 warning 放在顶层。

任何子结果截断时，顶层 `truncated` 也必须为 `true`。可继续读取时返回不透明 `next_cursor`；客户端不能解析或长期持久化 cursor。输出因硬预算停止但不可继续时允许没有 cursor，同时必须返回清楚的 warning。

## 兼容规则

- 客户端按 `version` 选择 decoder；同一 version 中忽略未知 JSON 字段。
- 未知 enum、status 或 error code 不是可忽略字段，必须拒绝。
- 字段只允许以向后兼容方式新增；删除、改名或改变含义需要新的 result version。
- JSON object 的字段顺序没有语义；golden 只锁定当前规范编码器输出。

完整事务失败后的各 statement 状态和继续执行规则见 [MSQL](./msql.md)。IPC frame 与连接 session 见 [本地 IPC 协议](../development/ipc-protocol.md)。
