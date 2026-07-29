# MSQL Batch 与事务边界 v1

状态：F12 Parser 与 F16b Batch Executor 已实现；F16c 将补齐 Parser 错误恢复和 daemon IPC 接线。

## Batch Parser

`parser.ParseBatch(source)` 在同一 Lexer token stream 上解析完整 request，返回 `memora.msql.ast/v1` Batch。不得先按字符串分号切分，因为字符串、quoted identifier 和注释可以包含分号。

一个 batch：

- 按源码顺序保存 statement；
- 参数 occurrence 在整个 request 内按源码顺序编号；
- 允许开头、结尾或两个 statement 之间出现空 statement；
- 仅包含 whitespace、注释和分号时返回 `empty_batch`；
- statement 之间缺少分号时返回 `unexpected_token`；
- 已定位到 statement 的 Parser 错误携带从 0 开始的 `StatementIndex`。

F12 在首个语法错误处停止形成 AST。F16 生成 Result Envelope 时，已定位错误必须进入对应 statement result；后续语句的恢复与 `skipped`/继续执行规则由执行层结合事务边界处理，不能把错误降级成模糊 request error。

## 事务 AST

以下写法进入同一事务边界 AST：

```sql
BEGIN;
START TRANSACTION;
COMMIT;
ROLLBACK;
```

`START TRANSACTION` 规范化为 `BEGIN`。事务 statement 的稳定 kind 和 action 均为 `BEGIN`、`COMMIT` 或 `ROLLBACK`。

多语句 request 不自动开启事务。事务外语句属于 autocommit；F12 只记录边界，不创建 Store transaction。

## Session 状态

每个长驻 IPC connection 持有独立 `TransactionState`：

```text
idle --BEGIN--> active
active --COMMIT/ROLLBACK--> idle
```

普通 statement 不改变状态。状态对象可跨多个 request 复用，并串行应用一个 batch 内的边界。

以下转换返回稳定的 `invalid_transaction_state` 语义和 statement index：

- active 状态再次 `BEGIN`；
- idle 状态执行 `COMMIT`；
- idle 状态执行 `ROLLBACK`。

同一 batch 在前面已成功发生的状态转换不会因后续非法边界自动撤销。例如 `BEGIN; BEGIN` 在第二项失败后仍为 active；F16 接入真实 Store transaction 后负责失败回滚策略。

短生命周期 CLI 退出和 IPC session 关闭时必须在 F16 回滚 active transaction，不允许把状态带到新连接。

## Batch Executor

F16b 的 request 将 SQL 与每条 statement 的结构化输入分离：

```json
{
  "request_id": "request-7",
  "source": "BEGIN; UPDATE work.notes SET title = :title WHERE row_id = :id; COMMIT",
  "statements": [
    {},
    {
      "parameters": {"named": {"title": "新标题", "id": "row_..."}, "positional": []},
      "mutation": {
        "expected_schema_version": 3,
        "expected_revision": 7,
        "max_affected_rows": 1
      }
    },
    {}
  ]
}
```

`statements` 可以整体省略；一旦提供，数量必须与解析后的 statement 数量严格一致。Parameter 和 mutation guard 不跨 statement 复用，也不插值到 `source`。

一个 Batch Session 串行执行 request，并持有跨 request 的 active Store transaction。事务内 Catalog 绑定、SELECT 和 Row mutation 都使用同一个 transaction scope，因此可以读取自己的未提交写入。session close 必须幂等回滚。

执行状态：

- 事务外每条语句 autocommit；失败项不阻止后续独立语句；
- 显式事务中的读失败返回 `failed`，但不自动中止事务；
- 显式事务中的写失败立即回滚；此前成功项改为 `rolled_back`，后续项和结束边界为 `skipped`；
- 越过被跳过的 COMMIT/ROLLBACK 后，后续事务外语句继续执行；
- 显式 ROLLBACK 自身为 `succeeded`，事务中此前已执行项为 `rolled_back`；
- BEGIN/COMMIT/ROLLBACK 的非法状态只让该边界 `failed`，不伪造 Store 状态转换。

F16b 在完整 AST 上执行。F16c 将已定位 Parser error 作为 statement result，并从 Lexer token 边界恢复后续 statement；在此之前无法形成完整 statement list 的解析失败仍是 request error。

## 关联

- [MSQL 标准语言](./msql.md)
- [MSQL Parser Core v1](./msql-parser.md)
- [MSQL Result Envelope v1](./result-envelope.md)
- [本地 IPC 协议](../development/ipc-protocol.md)
