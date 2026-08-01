# AI Story Gate v2

状态：F127 实现规格；当前无真实双宿主 evidence，产品级状态为 `INCOMPLETE`。

## 两层证据

第一层沿用 `memora.real-story-gate/v2`，证明发行 binary、双 adapter、MSQL 和原生存储的
机械闭环。它由 Go 预写每一步，不再单独代表 AI 用户故事 PASS。

第二层版本为 `memora.ai-story-gate/v2`：产品宪章中的 16 个原始 `US-*` 分别冻结一个
自然语言 Task 和一组结果检查；Codex、Claude Code 各完成一次。AI 报告同时绑定：

- Journey Suite、Real Host Contract、binary、Canonical Skill 和 MSQL protocol digest；
- 相同 story 的 host-independent Task digest；
- hostname-only host/model、completed receipt、tool/result/answer digest 和用量；
- 由独立 harness 产生的结果检查名、passed 状态与 evidence digest。

## 盲测与检查

Task 不包含 MSQL、Database/Table/Route/Row stable ID、预期检查名或标准答案。所有 Task
只授权 `work` Database，并继承 F123 的 64 tool calls、12,000 context characters 和
600,000 ms 上限。

每个 story 有独立检查，例如 RowID SQL evidence、revision/history、索引引用失效、
Schema migration 后旧数据、冲突并列与用户裁决、Source Receipt、重开恢复和确定性拒绝。
所有 mutation story 还必须包含 `rediscovered_from_root`。检查由 harness 根据数据库状态
产生，模型不能在 receipt 里声称自己正确。

## 当前边界

仓库只实现并验证 Suite/报告契约，不伪造真实 Provider 运行。F80 E2E 仍是 CI 必需的
mechanical prerequisite；在真实 Codex/Claude journey evidence 进入前，公开材料只能称
“运行时闭环通过、AI Story Gate INCOMPLETE”。

真实 harness 将 32 份 journey evidence 组成
`memora.ai-story-evidence-set/v1` 后，可构建产品级报告：

```text
go run ./cmd/build-ai-story-gate \
  --canonical-skill /abs/repo/skills/memora \
  --codex-runtime /abs/private/codex-runtime.json \
  --claude-runtime /abs/private/claude-runtime.json \
  --evidence /abs/private/ai-evidence.json \
  --output /abs/private/ai-story-gate.json
```

## 关联

- [Real Host Contract v1](./real-host-contract-v1.md)
- [F80 真实发行用户故事门（历史）](../archive/planning/f80-real-release-story-gate.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
