# F195：正式 MSQL 吸收提交面

状态：已完成，2026-08-05。

## 唯一主要结果

用正式 MSQL 表达资料吸收提案的结构审阅、批准提交和收据读取，使内置 Agent 只依赖
`protocol/msql.ExecuteMSQL`。F189–F194 的 Job、SourceStore、Document IR 与 coverage 仍归 Agent；
F195 只在最终语义模块即将进入用户 Database 时接管。

## 冻结语法

```sql
REVIEW ASSIMILATION FOR DATABASE work USING :proposal;
SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work;
SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work;
```

- `REVIEW` 是 L0、无写入的结构审阅：严格解码 proposal，逐条通过正式 Parser，限定为同一目标
  Database 的 L1 数据语句，校验参数、Schema/revision/affected-row guard 和 source provenance，
  返回 hash-bound、`review_required` 的规范 plan；
- `SUBMIT` 是 L1、仅 autocommit 的提交：重新验证完整 plan，并要求 action 为
  `SUBMIT_ASSIMILATION`、subject 为 plan SHA-256 的显式 approval；
- submitter 在独立 MSQL Session 内执行 `BEGIN → statements → COMMIT`，因此内部语句仍经过同一
  Parser、Policy、Binder、事务和执行器；不能调用 Row/Catalog/Store 私有 API；
- `SHOW ... RECEIPT` 是 L0，只能从语句显式指定且已授权的 Database 读取；
- proposal/plan/receipt 使用 `protocol/msql` 中立类型，Agent 生产代码不得 import 内核包；
- receipt 只保存 statement kind、affected rows、revision/commit sequence 和摘要，不保存参数、
  Row 正文、Document IR、ReadExtent、模型 prompt 或宿主上下文；
- 相同 plan 幂等返回 receipt；相同 plan ID 不同 digest 冲突；写调用后状态未知时 fail closed 为
  `in_doubt`，禁止盲目重放。

## 与后续 Feature 的边界

- F195 的 `REVIEW` 只证明 MSQL 结构、权限、guard 与来源字段可提交，不代表 AI 事实正确；
- F196 才产生带 source anchor/model/prompt/input digest 的 claim ledger 与候选语句；
- F197 才把未决问题接回用户交互分支；
- F198 才要求独立 author/reviewer 语义复核 artifact；
- F199 才用回读对账生成完整 Source Receipt 和可恢复的 in-doubt 决议。

## 旧路径

早期 `assimilation.record/submit/receipt` IPC 暂时保留外部兼容，但不是本 surface 的实现依赖，
也不进入 Agent import 或调用图。新提交链不得导入旧 coverage Processor/Controller。

## RED 与完成门

RED 命令：

```text
go test ./internal/msql/parser -run TestAssimilationMSQL
```

完成证据必须包含 Parser/AST 参数清单、L0/L1 scope、精确 approval、同库/语句 allowlist、嵌套
真实 MSQL 事务、receipt 无正文、幂等/冲突/in-doubt、Service protocol round-trip、Agent import
allowlist、`-race` 与全量 CI。

## 完成证据

- 三条语句拥有独立 AST，参数被完整计入 inventory；缺失 Database/USING/PLAN/IN 结构均由
  Parser 拒绝；
- protocol proposal 要求 coverage 完整、document/source digest、同一 author、Schema 与 affected
  guard；Seal 深拷贝嵌套参数并生成确定性 plan SHA-256；
- REVIEW 只接受目标 Database 内的 INSERT/UPDATE/DELETE/RESTORE/RELATE/UNRELATE L1 语句，
  多语句、事务、SELECT、结构操作、跨库和 revision guard 缺失均在写前失败；
- SUBMIT 重新执行完整 review，要求同库 L1 scope 与精确 `SUBMIT_ASSIMILATION` approval；显式
  外层事务内提交被拒绝并回滚；
- commit processor 在独立 protocol MSQL Session 执行 BEGIN/statement/COMMIT；同 plan 并发只派发
  一次，已知回滚可 guarded retry，transport 不确定持久为 in_doubt 且不盲重放；
- receipt 仅含摘要、kind、affected/revision/commit sequence；持久字节测试确认没有 MSQL、参数或
  语义正文，并验证 reopen、ID 冲突、跨库读取和 corruption fail closed；
- 真实 daemon/shared Service 链在没有旧 coverage task 的情况下完成 REVIEW→SUBMIT→Row INSERT→
  SHOW RECEIPT；目标 package、race、vet 与 Agent import guard 全绿。

完成门结论：PASS。下一项为 F196 可替换 Provider 的 draft/claim ledger。

## 关联

- [MSQL 标准语言](../query/msql.md)
- [Agent MSQL 依赖注入](../agent/agent-msql-dependency-injection.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
