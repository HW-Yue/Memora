# F204：外置 Agent Hook

状态：已完成；2026-08-05 规格冻结并通过完成门。

## 唯一主要结果

增加一个进程内、可选、默认关闭的外置 Agent Hook。它只接收 Memora 已生成的脱敏 TraceEvent，
附加显式的宿主/session/model/Skill/protocol 元数据，并提供有界、并发安全的快照。Hook 不读取
宿主完整上下文，不读取 API key，不接收 hidden reasoning 或默认正文，也不改变查询结果和数据库事务。

## 规则

- 外置宿主必须显式提供 `host_session_id`；缺失时使用稳定的 `unknown`，不能把 IPC 连接 ID 猜成 session；
- 事件只包含 Trace 的操作类型、稳定 digest、状态、usage、成本和耗时；正文永不进入 Hook；
- 每个 Hook 有最大事件数，超出后只增加 dropped 计数；不能无限缓存或阻塞 Agent 主链；
- 快照校验每个 TraceEvent、元数据和事件顺序，重复调用与并发追加结果可复现；
- Hook 是可观测性边界，不替代冻结 benchmark，也不自动计算写入时机或答案召回率。

## 完成门

先让 RED 测试在没有 Hook port 时失败，再验证 Query Agent 接入、脱敏、session 元数据、有界丢弃、
并发/race、取消和关闭行为。Hook 默认不启用；不执行真实模型或外部网络上报。

## 完成证据

- `QueryAgent.QueryWithHook` 是显式 opt-in 入口，Hook 只接收已脱敏的 `TraceEvent`；
- `ExternalAgentHook` 对事件数设上限，超出只累计 dropped，快照并发安全且按稳定键排序；缺失宿主 session 使用 `unknown`；
- 覆盖摘要脱敏、Query Agent 集成、未知 session、并发追加和 race；本次没有真实模型调用或网络上报。

## 关联

- [内置评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
- [查询 Session](./f186-query-session.md)
- [F203 OCR/视觉路径证据门](./f203-ocr-evidence-gate.md)
