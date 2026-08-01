# F127 AI Story Gate v2 开工与完成门

状态：已完成（2026-08-01）；真实 evidence 状态为 `INCOMPLETE`。

## 单一主要结果

把现有预写 MSQL 的机械运行时门降级为 prerequisite，并新增每个产品 `US-*` 都由
Codex 与 Claude Code 真实 Agent Task receipt 和冻结结果检查共同证明的产品级门。

## 产品门

- “命令能跑”不再等价于“AI 自主完成故事”；两层证据必须同时存在；
- Task 只给自然语言目标、授权 scope 和预算，不给预写 MSQL 或正确对象 ID；
- 每个 story 的结果检查按产品语义冻结，host 不提交自评分；
- Provider 凭据、回答正文和 hidden reasoning 不进入报告；
- 无真实双宿主 evidence 时状态为 `INCOMPLETE`，不能沿用旧 PASS。

结论：PASS。

## RED 清单

入口：`go test ./internal/storygate`

1. F80 mechanical report 可单独获得产品级 PASS；
2. 16 个 story 任一缺 Codex/Claude evidence，或 Task digest 跨宿主不同仍通过；
3. prompt 泄漏 MSQL、stable ID 或 expected check，或 scope/预算未冻结；
4. receipt 的 Skill/MSQL/Task contract/host/model/status/tool-call 不匹配仍通过；
5. required outcome check 缺失、重复、失败或 evidence digest 无效仍通过；
6. mutation story 未证明从顶层 rediscovery，仍可 PASS；
7. strict JSON、suite/report hash、mechanical pair digest 篡改不能检测。

首次 RED 预期因 AI Journey Suite 与 product-level report 尚不存在而失败。

## 明确不做

- 不在 Memora 内调用 Provider 或管理凭据；
- 不用 scripted fake 生成 checked-in PASS；
- 不在 F127 实现尚缺失的语义 DBA、Schema migration 或持续输入能力；
- 不删除 F80 mechanical runtime gate。

## 完成门

- Suite、双宿主矩阵、receipt、outcome checks、mutation rediscovery 与 strict report 全绿；
- 现有 F80 E2E 继续全绿，但文档不再把它称作真实 AI PASS；
- targeted race 与 `./scripts/ci.sh` 全门通过；
- 当前证据状态明确记录并推进 F128。

## 完成证据

- RED：新增 AI Suite/Report 测试首次因全部生产 API 缺失而编译失败。
- Suite：16 个 `US-*` 均有稳定自然语言 Task 和独立 required checks；prompt 泄漏 MSQL、
  stable ID 或 check name 的门通过，Task scope/预算绑定 F123。
- 双宿主：每个 story 精确要求 Codex + Claude Code 两份 evidence，共 32 份；Task digest
  host-independent，receipt 的 host/model/Skill/protocol/status/tool budget 全部重验。
- 结果：required checks 必须顺序一致、全部 passed 且绑定 evidence digest；全部 mutation
  story 明确要求 `rediscovered_from_root`。
- 分层：F80 v2 mechanical pair digest 成为 prerequisite；AI v2 报告同时绑定 binary、
  adapters、Suite、contract 和 journeys，strict JSON/hash/原子 `0600` 发布通过。
- 当前证据：未生成 checked-in synthetic PASS；无 32 份真实 Provider evidence，因此公开
  产品状态明确为 `INCOMPLETE`，仅机械运行时闭环 PASS。
- 完整门：targeted race 与 `./scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、
  e2e、arm64/amd64 cross-build 全部通过。
