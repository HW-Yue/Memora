# F123 Real Host Contract 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-HUMAN、US-COLD、US-READ、US-DEVELOPER；
- 用户结果：Codex、Claude Code 与 Kimi Provider 可执行完全相同的 Skill/Task；
- 唯一主要结果：版本化、可摘要校验的真实 host task/invocation/receipt contract；
- 作用边界：只定义宿主输入、预算、证据与隐私，不调用模型、不创建 benchmark corpus；
- Kimi 定位：Provider，不复制第三套 Skill 或 Memora adapter；
- 开工前结论：PASS。

## RED 清单

- Canonical Skill 只有 protocol digest，没有独立 Task contract 或 digest；
- Codex/Claude adapter 可绑定不同题目、预算或 scope，却仍被判为兼容；
- Kimi 被误建模成新的数据库/Skill host，或 Provider 凭据进入 Memora contract；
- Task digest 混入 host/model 导致无法公平比较；
- prompt/scope/tool/context/time 无硬上限；
- receipt 保存完整模型输出、hidden reasoning、endpoint URL 或秘密；
- Story Gate pair 只比较 Skill/protocol，不比较 Task contract。

RED 命令：

```text
go test ./internal/hostcontract ./internal/codexadapter ./internal/claudeadapter ./internal/storygate
```

RED 已因 `internal/hostcontract`、`host-contract.json`、adapter task digest 和 Story Gate
绑定均不存在而失败。

## 完成门

- strict JSON load、Task/Invocation/Receipt/Matrix validation 与 digest determinism；
- 两个 adapter v2 同源携带 contract，checked-in bundle 无 drift；
- Story Gate report/pair 绑定相同 Task contract digest；
- corruption/unknown field/预算/秘密形态/跨 digest matrix 均有拒绝证据；
- `scripts/ci.sh` 全绿并同步设计与规划；证据满足前保持 `INCOMPLETE`。

## 完成证据

- RED 先证明 hostcontract package、Canonical host contract、两个 adapter digest 和
  Story Gate 字段均不存在；
- strict JSON 与 deterministic digest 覆盖 Task、Invocation、Receipt，拒绝 unknown
  hidden reasoning、带凭据 URL、错 scope、错序命令、混 digest 与工具/上下文/时间超预算；
- 标准 matrix 要求 Codex、Claude Code 和 `provider=kimi`，同时证明 host/model 不进入
  Task digest，Kimi 不产生第三套 adapter；
- Codex/Claude adapter manifest v2 携带同一 host contract 文件和 digest，checked-in
  bundle 与 generator 完全一致；
- 真实发行 binary 的 Codex/Claude Story Gate v2 双旅程通过，并绑定相同 Skill、MSQL
  protocol 与 Task contract digest；
- race、integration、e2e、全仓 CI 与 cross-build 通过，完成结论为 `PASS`。
