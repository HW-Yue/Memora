# Route Trace Auxiliary Store v1

状态：F114 已完成并验收；2026-08-01 冻结 auxiliary store 与 retention epoch。

## 唯一职责

保存短期、可清理、严格预算的 Route navigation receipt。它使用 daemon 已有 auxiliary
store 的独立 bucket，不属于用户语义 Database 的真相源；删除 trace 不改变任何 Row、
Catalog、Route、History、Change 或查询结果。

## Envelope

`memora.route-trace/v1` 包含 trace ID、全 Instance trace sequence、recorded/expiry time、
actor、Database/Table ID、终态、确定排序的 steps 和 SHA-256 checksum。

每个 step 最多携带 24 个候选 RouteID 或 locator；每笔 trace 最多 64 steps。允许字段
只有稳定 ID、revision、operation、result code、elapsed milliseconds 和 remaining budget。
字符串、数组、encoded envelope 均有硬预算，JSON 必须 canonical 且拒绝未知字段。

## 原子性与索引

- body key 按零填充 sequence 排序；trace ID 二级 key 定位 body；
- body、ID locator 和 meta high-water 在一个 auxiliary transaction 中提交；
- duplicate trace ID 只允许 checksum 完全相同的幂等 retry；
- timeline snapshot 固定 high-water 和 retention epoch；
- prune 在一个 transaction 中删除 body/locator 并推进 retention epoch，high-water 不回退。

## 故障边界

丢 body、错 locator、sequence/ID/checksum 不一致、非 canonical JSON 或 meta 回退均视为
corruption，产品读取失败关闭。不得扫描 Security Audit、Change Log 或 Database body
作为 fallback。
