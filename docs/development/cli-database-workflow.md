# CLI Database Workflow v1

状态：F27 已冻结本地 `exec`、`query`、`doctor` 与 daemon 垂直链路。

## 命令

所有数据库操作都通过当前 Instance 的 Unix socket；CLI 不打开 Store 或数据库文件。

```text
memora exec  [--data-dir PATH] [--input JSON] "MSQL"
memora query [--data-dir PATH] [--input JSON] "MSQL"
memora mutate [--data-dir PATH] --plan 'MUTATION_PLAN_JSON'
memora doctor [--data-dir PATH]
```

`exec` 接受任一已支持的 MSQL batch。`query` 只接受 SHOW、DESCRIBE、SELECT、MATCH 和 OPEN ROUTE，避免误把 mutation 放在只读入口执行。两者都输出完整 `memora.result/v1` JSON envelope；statement 失败时仍输出 envelope，并以非零进程码结束。

`--input` 是单条 statement 的严格 JSON：

```json
{
  "parameters": {
    "named": {"row": "row_...", "terms": ["architecture"]}
  },
  "mutation": {
    "expected_schema_version": 1,
    "expected_revision": 2,
    "max_affected_rows": 1,
    "index_terms": ["architecture"],
    "route_leaf_ids": ["route_..."],
    "actor": "agent:codex",
    "source": "conversation:event-7",
    "reason": "refine decision"
  }
}
```

未知 input 字段、缺少 MSQL source 或多余 source 都是 usage error。多 statement 且每条需要不同 input 的事务仍由长连接 IPC/宿主 API 提交，CLI v1 不把 JSON 数组再发明成另一种 batch 语言。

`mutate` 是 F31 的宿主写入入口。它严格解码 `memora.mutation-plan/v1`，在
任何 daemon 调用前执行 Skill Policy，并依次发出 preflight、一个 mutation
batch 和 verify。MERGE/SPLIT 的不同 StatementInput 由 Plan 编译进同一显式
事务，不改变 `exec --input` 的单 statement 边界。输出为
`memora.mutation-receipt/v1`。

## 统一执行

`msql.execute` 现在在同一 Batch Session 中路由：

- Catalog Binder：CREATE/ALTER/SHOW/DESCRIBE Database、Table、Column；
- Row Executor：INSERT/UPDATE/DELETE/RESTORE；
- Query：SELECT、MATCH、History、Relation、Router。

DDL 与数据操作共享 Lexer、Parser、stable result envelope 和 daemon 持久 Store，不存在 CLI-only DDL 或测试专用写入接口。

## Doctor

`doctor` 让 daemon 在一致读事务中构造 Logical Snapshot，执行完整 identity/revision/reference 校验和 canonical SHA-256，并返回：

```text
status, snapshot_version, snapshot_hash,
databases, rows, history, relations
```

Catalog 或权威记录损坏时 doctor 失败，不伪装为 healthy。当前计数只针对权威逻辑对象；派生索引质量由重建与 Phase B 退出测试单独验证。

## 关联

- [本地 IPC 协议](./ipc-protocol.md)
- [Logical Snapshot v1](../storage/logical-snapshot-v1.md)
- [MSQL Result Envelope v1](../query/result-envelope.md)
