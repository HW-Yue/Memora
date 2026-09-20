# F187：Agent Write Profile 与审批信封

状态：已完成，2026-08-05。

## 唯一主要结果

为内置 Agent 增加一个 Policy 强制的 L1 Write Gateway。Agent 先提交无权限的 MSQL draft，Gateway
锁定 scope、actor、Schema/revision/affected-row guard 并生成规范 proposal digest；只有用户确认的
一次性审批信封与该 digest 完全匹配时，Gateway 才通过 F175 MSQL port 执行一次。

F187 不调用模型、不实现网页写入工作流、不允许 Schema/L2 或不可逆/L3 操作，也不新增数据库
旁路。F188 才使用该边界完成短文本 draft→approve→commit→SELECT verify。

## 固定旅程

```text
Agent WriteDraft（source + parameters + mutation guards，无 Authorization）
→ WriteGateway.Prepare
  → 覆盖 actor
  → 固定 authorized databases / 每库 L1
  → 校验 expected_schema_version、可选 expected_revision、max_affected_rows
  → proposal SHA-256（绑定 profile、proposal ID 与完整 MSQL）
→ 等待用户确认；此时没有数据库调用或事务
→ WriteApproval（proposal SHA-256 + approver + confirmed）
→ ExecuteApproved
  → 重新计算 digest、校验无篡改
  → 注入 hash-bound `memora.approval/v1`
  → 同一 MSQLExecutor 恰好一次调用；审批立即消费
```

## 强制边界

- Profile 只授予指定 Database 的 L1；draft 中伪造的 actor、Authorization 或 approval 没有输入面；
- 每个 statement 必须有非零 `expected_schema_version`、1..Profile 上限的
  `max_affected_rows`、reason、source 和已知 SourceKind；UPDATE/DELETE 是否还需
  `expected_revision` 继续由 Parser/Executor 按真实语句强制；
- Gateway 不解析 MSQL、也不复制 Policy；CREATE/ALTER/跨库/L3 即使藏在 source 中，最终仍由引擎
  根据 Gateway 注入的 L1 scope 拒绝；
- proposal 任一字段被改动后 digest 校验失败；审批不能授权相似 proposal；
- 审批在调用 MSQL 前原子消费；成功、失败、取消或结果未知都不能盲重放。同一业务意图需重试时
  必须生成新的 proposal ID 并重新审批；
- Prepare 与等待审批期间不调用 MSQL、不持有事务；Execute 只提交一个完整 batch；
- Agent 生产代码仍只能 import `protocol/msql`，不得接触 Parser、Policy、Store 或 Instance。

## RED 与完成门

- RED 先证明 Profile、Proposal、Approval 和 Gateway 尚不存在；
- malicious draft 的权限被替换为固定 L1 scope，actor 与所有 guard 被锁定；
- 缺失/错误/未确认审批、proposal 篡改和已消费审批均零 MSQL 调用；
- 同一审批并发执行只有一个调用者进入 MSQL，race 全绿；
- 真实 MSQL Service 证明获批 INSERT 成功，藏在 proposal 中的结构操作被 L1 Policy 拒绝；
- Agent import allowlist、定向/集成/race 与完整 CI 全绿。

用户执行授权：2026-08-05 用户要求持续执行至 F204；真实模型限速时一次成功链路证据即可。

## 完成证据

- RED：Profile、Proposal、Approval 与 Gateway 均不存在，单元/Service 集成测试编译失败；
- GREEN：Prepare 零 MSQL 调用并锁定 L1 scope、actor 和 guards；proposal/原 draft 深拷贝隔离；
- 缺失、错误、未确认、篡改和已消费审批全部在 MSQL 前失败；16 个并发执行者只有一个进入 MSQL，
  定向 race 全绿；
- 真实 MSQL Service 中获批 INSERT 成功落库，隐藏 `CREATE TABLE` 被引擎 L1 Policy 拒绝；
- Agent import allowlist、全量 race/integration/e2e/cross-build 与 `scripts/ci.sh` 全绿。

完成结论：PASS。
