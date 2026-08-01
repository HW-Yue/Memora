# F124e Speculative Discovery Skill 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

Canonical Skill 能在一个 Agent 回合规划、并行执行和审计有界 Catalog + predictor +
同主题根 Route 预取，并在任何误预测或失效状态下确定性回到普通 Router。

## 产品门

- 只减少 LLM 续推；不把策略放入存储引擎或隐藏 Planner。
- 候选/预取只用于导航，最终证据仍是 revision-matched SELECT Row。
- 任何零命中、unavailable、stale 和错误预取都不缩小可选 Table 集合。
- Canonical Skill 是唯一来源，Codex/Claude adapters 必须重新确定性派生。

结论：PASS。

## RED 清单

入口：`go test ./internal/skilldiscovery ./internal/skillcontract ./internal/codexadapter ./internal/claudeadapter`

1. 规划在每个工具结果后要求新模型决策，不能同回合并行；
2. Lexical + Vector 各自获得完整预算，合计越过候选/bytes 上限；
3. 未授权 Database 或超过两个 Table root 被预取；
4. predictor catalog revision 不一致仍被合并，调用数/输出 bytes/page snapshot 不可审计；
5. 候选或 root 被标成 answer evidence；零命中 Table 无法显式选择；
6. 错误预取不产生普通 root fallback，旧 topic Frame 被复用；
7. Canonical Skill、contract、golden 与双宿主 adapter 漂移。

首次 RED 预期因 `internal/skilldiscovery` 与 speculative contract 尚不存在而失败。

## 明确不做

- 不实现模型调用、query embedding 或自动 Table/Route 选择；
- 不新增引擎复合 Planner、事实缓存或动态 system prompt 索引；
- 不把 predictor score 跨 kind 比较；
- 不改变 Router、Row 或 storage format。

## 完成门

- plan/reference、预算、snapshot、错误预取、topic invalidation 与 navigation-only 测试全绿；
- Canonical Skill quick validation、contract/golden 和双 adapter drift/e2e 全绿；
- `./scripts/ci.sh` 全门通过；
- 规划推进到 F125 Benchmark Runner。

## 完成证据

- RED：首次 `go test ./internal/skilldiscovery` 因 package 无生产代码失败；新增 Canonical
  contract 测试随后逐项报告 profile、回退与 navigation-only 规则缺失。
- Plan：同回合 Catalog/Table/Lexical/Vector/root calls、4/4 与 2048/2048 确定性分配、
  精确授权、输入 deep-copy、最多四库/两 root/十 calls 全绿。
- Frame：不同 predictor snapshot 独立、Catalog revision 一致、page snapshot、输出 bytes、
  全局用量与 truncation 可审计；超 context 后 Frame 失效。
- 回退：候选 `AnswerReady=false`；零命中 Table 仍可选；错误预取与旧 topic 都生成普通
  `SHOW ROUTES ... AT ROOT` fallback。
- 真实链路：native Catalog/Route、Lexical、active Vector generation 和 root page 组成的
  完整计划可执行，结果不含 Row locator 或事实 evidence。
- Skill：`skill-creator` quick validation 通过；contract/golden、Codex/Claude 确定性
  adapters 与 e2e drift 门通过。
- Race：skilldiscovery、skillcontract、双 adapter、native 与 CI guard targeted race 全绿。
- 完整门：`./scripts/ci.sh` 通过 format、vet、unit、全仓 race、integration、e2e 与
  amd64/arm64 cross-build。
