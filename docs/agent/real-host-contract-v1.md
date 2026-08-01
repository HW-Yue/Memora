# Real Host Contract v1

状态：F123 已完成；2026-08-01 冻结并通过双宿主运行时验收。

## 目的

真实模型评测使用同一份自然语言 Task、Canonical Skill 和 MSQL/Result contract，宿主或
Provider 只作为执行维度，不能偷偷改题、放宽预算或换数据 scope。

`Codex` 与 `Claude Code` 是 host surface；`Kimi` 是可由宿主管理的 Provider，不是第三套
Memora adapter。Kimi 可经 CC Switch 等配置暴露为 OpenAI-compatible 或
Anthropic-compatible endpoint；endpoint 与凭据始终属于宿主。

机器契约位于 `skills/memora/host-contract.json`，版本包括：

```text
memora.real-host-contract/v1
memora.real-host-task/v1
memora.real-host-invocation/v1
memora.real-host-receipt/v1
```

## Task 与执行矩阵

Task 只含 task ID、自然语言 prompt、授权 Database、corpus digest 和本次硬预算。Task
digest 不含 host、Provider、model 或 endpoint，因此同一题跨宿主必须得到同一 digest。

Invocation 另行绑定：

- Codex 或 Claude Code surface；
- host-managed Provider 名称、兼容协议、model 和仅 hostname 的 endpoint evidence；
- Canonical Skill、MSQL protocol 和 Task digest。

标准兼容矩阵至少包含 Codex、Claude Code 和一个 `provider=kimi` 的 invocation；三者
必须共享 Task/Skill/protocol digest。缺少真实凭据不伪造运行结果，由 F125 Runner 受控
执行时再产生 receipt。

## 预算与收据

- prompt 最多 4,096 字符、Database scope 最多 32 个；
- 单任务最多 64 次 Memora tool call、12,000 个上下文字符、600,000 ms；
- 允许的工具命令与 Canonical Skill 当前九个逻辑入口完全一致；
- receipt 只记录顺序、命令、Result digest、字符/token/耗时计数与 answer digest；
- 不记录 API key、Bearer token、完整 endpoint URL、hidden reasoning 或模型草稿。

Codex/Claude adapter v2 都携带同一个 `host-contract.json` 和 digest；Story Gate 报告也
绑定该 digest。F123 只冻结与验证契约，不调用 Provider、不比较模型质量、不创建题库。

## 关联

- [Canonical Skill v1](./canonical-skill-v1.md)
- [宿主模型与 CC Switch 兼容边界](./host-provider-compatibility.md)
- [Route Retrieval Benchmark v3](../development/route-retrieval-benchmark-v3.md)
