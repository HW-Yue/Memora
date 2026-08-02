# F178：Agent Event / Trace / Usage 信封

规划状态：已通过单项 Review，批准按 RED → GREEN → REFACTOR 实现。

## 唯一主要结果

冻结可重放、正文脱敏的 Agent Trace contract 与并发安全 recorder，在第一次真实模型调用前统一
run/session/turn、Provider、MSQL/tool、token、费用和分段耗时口径。F178 不接真实 Provider，
不实现 Query loop、不持久化 Trace，也不迭代 Admin。

## Event

每个完成事件携带版本、run/session ID、严格递增 sequence、turn、kind/operation、开始/结束 UTC、
duration、status/error code、输入输出 byte count 与 `sha256:` digest。Provider 事件可附 model 和
标准 token usage；费用使用显式 currency、整数 micros 与 price snapshot ID，未知费用保持 absent。

v1 kind 为 `bootstrap|provider|msql|tool`，status 为 `succeeded|failed|cancelled`。事件没有 prompt、
message content、Row、MSQL source/parameters、tool arguments/result、API key、endpoint、header、
hidden reasoning 或错误正文的字段。

## Recorder 与 Trace

- recorder 由显式 run/session identity 构造，`Append(Draft)` 原子分配 sequence；
- Draft 时间、turn、operation、digest/bytes、Provider usage/cost 必须自洽；
- Snapshot 返回事件副本与确定性 Summary：事件/状态/kind 计数、token、费用和各 kind duration；
- 并发 Append 的 sequence 唯一连续，Snapshot 不暴露内部 slice/map；
- recorder 不读取时钟、不猜模型价格、不计算重叠 wall time，也不执行外部 I/O。

## 完成证据

- validation 覆盖 identity、时间、digest/bytes、status/error、usage/cost 与禁用组合；
- replay JSON round-trip 稳定，Summary 可由 events 重算对拍；
- 并发 Append + Snapshot 在 race 下 sequence 连续且副本隔离；
- 无正文/secret 字段的反射 contract 测试和敏感 fixture 泄漏扫描；
- Agent import allowlist、unit/race/vet 与完整 CI 全绿。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F178 范围。

开工前结论：PASS。
