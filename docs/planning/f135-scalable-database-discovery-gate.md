# F135 Scalable Database Discovery 开工门

状态：已完成（2026-08-01）。

## 单一主要结果

Agent 用一个扁平、分页、带字节预算的 Catalog Atlas 发现任意授权 Database 及其 Table，
并能用稳定覆盖状态确定性续页到冷库，而不是受首屏或 predictor 零命中排除。

## 产品门

- 新增 `SHOW CATALOG ATLAS [CURSOR ...] [LIMIT ...] [BYTES ...] COMPACT`；
- Atlas 按 Database 后接其 Table 的稳定顺序返回扁平 semantic metadata，不展开 Column、
  Route 或 Row，不重复整库嵌套对象；
- 每页同时受 entry limit 和 UTF-8 JSON row bytes 限制，返回 snapshot、next cursor、
  truncated；同 cursor 跨 scope、篡改或 Catalog drift 必须拒绝；
- authorization 在组装 Table 前生效，未授权 Database/Table 不进入 snapshot 或输出；
- Speculative Discovery 改用 Atlas，支持至多 32 个精确授权 Database，不再为每库产生
  一个工具调用；Lexical/Vector 仍只是同回合候选，不能证明零命中库不相关；
- Atlas coverage 明确区分 partial/complete；partial frame 能无模型决策地生成下一页调用，
  complete 才能声称已覆盖全部授权 Catalog；
- 冷库可通过续页到达，预测错误只影响速度，不影响可发现性。

## RED 清单

1. Parser/Executor 尚无 `CATALOG ATLAS` 或 byte budget；
2. 多库发现需要 N 个 `SHOW TABLES` tool calls，四库以上被拒绝；
3. 首屏截断却没有 coverage/continuation，冷库被静默漏掉；
4. byte limit 只限制行数，超长 metadata 可突破上下文；
5. cursor 可跨授权、跨请求或 Catalog revision 复用；
6. 未授权 Database 的 Table 在过滤前进入可观察输出/snapshot；
7. predictor 零命中被误作 Catalog 排除条件；
8. Atlas 泄露 Column、Route、Row 或正文。

## 明确不做

- 不让引擎自动选择 Database/Table，不新增隐藏 planner；
- 不承诺单页装下无限 Catalog；大 Catalog 必须按 coverage 续页；
- 不把 Atlas 长期写入 system prompt；它仍是当前 topic 的 Route Frame；
- 不改变 Lexical/Vector/Router 的导航-only 权威边界。

## 完成门

parser/binder/executor、reference ordering、limit+bytes、authorization、cursor drift/corruption、
cold-page coverage、32库 profile、predictor fallback 与 targeted race 全绿；全仓 CI 后进入 F136。

## 完成证据

- Parser/Executor 提供强制 COMPACT 的 `SHOW CATALOG ATLAS`，reference 测试覆盖稳定
  Database→Table 顺序、空库条目、冷库末页与 Column/Route/Row 不泄露；
- entry limit 与精确 rows JSON byte budget 同时生效，过小/过大预算稳定拒绝；
- authorization 在 `ShowTables` 前过滤，未授权 Catalog 变化不污染 snapshot，授权 scope
  或 Catalog drift、cursor 篡改均失败；
- Speculative Discovery v2 对 32 个授权库仍只有 Atlas + predictor 调用，partial coverage
  能确定性生成 next call，首屏外 Table 仍可走普通 Router fallback；
- native MSQL、Canonical Skill、Codex/Claude adapters 与 targeted race 全绿；
- `./scripts/ci.sh` 通过 format、vet、unit、race、integration、e2e 和 cross-build。

下一项：F136 Policy Enforcement v2。
