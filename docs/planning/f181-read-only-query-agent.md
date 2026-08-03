# F181：只读 benchmark Query Agent

规划状态：已完成；RED → GREEN → REFACTOR 与完整 CI 通过。

## 唯一主要结果

在 `internal/agent` 组装一个显式、有界、只读的 benchmark Query loop：它复用 F176 Bootstrap、
F177 Provider、F178 Trace，数据库能力只来自 F175c `MSQLExecutor`；成功结果同时返回最终答案、
真实 `SELECT` evidence 与完整 Trace。F181 不接 daemon/CLI、不提供 `memora ask`、不写库、
不做 durable session/checkpoint，也不实现 corpus、评分器或 Provider adapter。

## 固定状态机与上下文

1. 在第一次模型调用前组装 Query Bootstrap Frame；
2. LLM 首轮读取问题、完整有界 Bootstrap，并且必须调用一次 `execute_msql`；
3. 每轮只接受一个 tool call，参数是一条完整 MSQL batch 的 source 与逐 statement parameters；
4. 未得到成功 `SELECT` 时继续导航；每个后续请求只携带问题、上一 tool call 与当前 Envelope，
   不重复整个 Atlas 或无限累积旧 transcript；
5. 得到至少一个成功 `SELECT` 后，下一轮固定 `tool_choice=none`，只能生成最终答案；
6. 无 SELECT、空答案、`length`、越步数或协议不合法都显式失败，不把 lexical/Route 位置当事实。

模型应把可并行的 Route/SELECT 组成同一个 MSQL batch；loop 不并行执行多个模型 tool call，
不自动重试 Provider 或 MSQL。最大 Provider 调用、工具调用、参数/结果/答案 UTF-8 bytes 和输出
token 都由请求预算限制。

## MSQL 与只读边界

- tool arguments 不接收 Authorization、Mutation、request version 或 secret；Agent 为每个 statement
  注入调用方 Database scope，并把 default/override 全部降为 L0、移除 Approval；
- 参数严格 JSON 解码，拒绝 unknown/trailing、空 batch、超数量与超 byte；
- source 仍由正式 MSQL Parser/Policy 判定，Agent 不复制 SQL parser；仅做保守事务控制词扫描，拒绝
  quoted/comment 之外的 `BEGIN|START TRANSACTION|COMMIT|ROLLBACK`，确保模型等待期间不持有事务；
- 每次工具调用只提交一次完整 Request；返回 Envelope 必须通过中立协议校验并受结果 byte 门约束；
- evidence 只收集引擎标记为 `Statement="SELECT"` 且 `status=succeeded` 的实际 StatementResult，
  保存 request ID 与结果副本；Trace 仍只保存 body digest，不保存问题、MSQL、Row 或答案正文。

## 注入、结果与 Trace

`QueryAgent` 通过构造函数接收 `MSQLExecutor`、`Provider` 和窄 `Clock`，无全局注册表。请求携带
run/session identity、问题、model、Authorization、Bootstrap/loop budget；结果版本化并含 answer、
evidence、Trace。失败也返回截至失败点的可验证 Trace 和稳定错误类型。

Trace 覆盖 Bootstrap、其中每次 MSQL、每次 Provider、tool decode/execute 与模型发起的 MSQL；
成功链必须能由 sequence、turn、kind、usage、duration 和 digest 重放。Provider 失败无 usage 时记录
零值 usage，不猜价格；F181 不计算费用。

## 完成证据

- scripted MSQL/Provider 精确重放 `Bootstrap → navigate → SELECT → final`，并证明后续上下文不含 Atlas；
- 多 statement SELECT evidence、L0 Authorization 降级、参数注入与事务词保守拒绝；
- 无 SELECT 提前回答、多个 tool call、tool name/JSON/trailing、结果过大、length、步数耗尽均失败；
- Provider/MSQL error 与 context cancel 不重试，失败 Trace 完整且正文/secret 扫描无泄漏；
- import allowlist、unit/race/vet、完整 CI 与 cross-build 全绿，Agent 测试不打开 Instance。

用户执行授权：2026-08-03 用户要求持续顺序完成全部已讨论 Feature。本 Review 只批准上述 F181 范围。

开工前结论：PASS。

## 完成结论

- `QueryAgent` 已实现 `Bootstrap → navigate → SELECT → final` 有界状态机，首轮之后只保留问题、
  上一 tool call 和当前 Envelope，不重复 Atlas 或累积全部 transcript；
- `execute_msql` 严格解码完整 batch，Agent 注入 L0 Authorization 并移除 Approval；显式事务控制在
  MSQL 调用前拒绝，引号、quoted identifier 与注释中的同名文本不会误判；
- 只有引擎返回的成功 `SELECT` StatementResult 能形成防御性 evidence；最终调用固定禁止工具，
  提前回答、空答案、`length`、越步数、超 byte 或无 evidence 均失败；
- Bootstrap/MSQL/Provider/tool 全链进入正文脱敏 Trace；Provider/MSQL error 和取消不重试，失败点
  仍返回可验证 Trace；整数参数与 evidence 类型不经浮点强转；
- scripted transcript、上下文压缩、multi-SELECT、L0 降级、wire 畸形、预算、取消、泄漏扫描、
  import allowlist、unit/vet/race、完整 integration/e2e 与独立 cross-build 全绿。

F181 的确定性实现和 fake 验收只依赖 F176–F179 contract，不需要某个厂商 adapter；使用真实 Kimi
运行仍依赖 F180 通过真实鉴权 smoke。F181 不因此成为 `memora ask` 产品入口。

完成门结论：PASS。下一项可独立 Review F182 answer corpus；F180 真实鉴权证据仍单独待完成。
