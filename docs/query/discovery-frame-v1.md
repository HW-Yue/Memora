# Discovery Frame v1

状态：F124a 已实现并冻结；由 F124b–F124e 复用，当前不包含预测算法。

> **已被 v2 取代（2026-08-25）。** Frame 瘦身完成：`score`／`score_kind`／
> `reason`／`matched_fields`／`Budget` 四元组／`PredictorReceipt` 全部去掉，
> 候选只剩 `database_id` + `table_id` + `path`，版本号提到
> `memora.discovery-frame/v2`。旧字段是**删掉**而不是保留不填——
> 一个永远为空的字段是个会被下游当真的谎。
> 本文只作为 v1 的历史记录保留，**不描述当前代码**。
> 当前形态见 [`internal/discovery/frame.go`](../../internal/discovery/frame.go)
> 与[候选预测器只给路径](./predictor-path-only-v1.md)。

## 目的

一次 Discovery 可以组合 Catalog、字面位置、Route 向量或会话线索，但这些输出只用于
猜测下一步该读哪棵 Table Router。Frame 不是事实结果，也不能代替 `OPEN ROUTE` 后的
RowID SQL 回表。

## Envelope

成功的 statement result 可附加：

```json
{
  "discovery": {
    "version": "memora.discovery-frame/v1",
    "usage": "navigation_only",
    "snapshot": "sha256:...",
    "catalog_revision": "sha256:...",
    "budget": {
      "candidate_limit": 8,
      "utf8_byte_limit": 4096,
      "candidates_used": 3,
      "utf8_bytes_used": 640
    },
    "predictors": [],
    "candidates": [],
    "truncated": false
  }
}
```

`snapshot` 固定整个 Discovery 读取视图；`catalog_revision` 固定当前授权 scope 内的
Catalog 版本。组装器拒绝混入其他 snapshot 或 Catalog revision 的批次。

`candidate_limit` 与 `utf8_byte_limit` 是所有 predictor 共用的硬预算，不是每个子查询
各自获得一份 LIMIT。`utf8_bytes_used` 是最终 `candidates[]` 各元素紧凑 JSON 的 UTF-8
字节数之和；任一硬预算阻止继续接收候选时 `truncated=true`。

## Predictor receipt

每个尝试过的 predictor 产生一条 receipt：

- `predictor`：稳定、小写、可带 `/vN` 的来源标识；
- `status`：`succeeded` 或 `unavailable`；
- `score_kind`：`none`、`match_count` 或 `dot_product`；
- `reason`：有界的人类可读依据或不可用原因；
- `candidate_count` 与 `truncated`：实际进入 Frame 的数量及本来源是否被全局预算截断。

`unavailable` 是可回退预测器的正常收据，只允许零候选，不把 statement 或 request 标成
失败。语法、权限、snapshot 冲突等真正查询错误仍使用 Result Envelope 稳定错误码。

## Candidate

候选携带 `database_id`，并可逐级增加 `table_id`、`route_id` 与必需的
`route_revision`。禁止跳级位置；Route 候选必须完整绑定 Database/Table/Route。

每项还携带 `predictor`、`reason`、`score_kind` 和可选 `score`。`score` 仅在非 `none`
类型出现且必须有限；不同 score kind 不可直接比较。predictor 可增加排序去重后的
`matched_fields` 说明命中的语义字段，但不得回显 query 或正文。相同 predictor 不得
重复同一位置。

候选不含 RowID、Row 正文、snippet、embedding 或答案字段。客户端必须把
`usage=navigation_only` 当作权威边界，并继续读取 Router 和 SQL Row。

## 兼容与边界

- Discovery Frame 只允许挂在成功 statement；其截断必须传播到 statement 和顶层；
- decoder 在 v1 忽略未知 JSON 字段，但拒绝未知 version、usage、status 和 score kind；
- predictor 字符串可扩展，不能包含密钥、query 正文或 Provider 私有信息；
- F124a 不冻结 MSQL 语法，不实现 lexical、vector、prefetch 策略或持久化格式。

## 关联

- [MSQL Result Envelope v1](./result-envelope.md)
- [语义路由投机预取](./speculative-route-prefetch.md)
- [Route Predictor 历史 Feature 计划](../archive/planning/route-predictor-feature-plan.md)
- [F124a 开工与完成门](../archive/planning/f124a-discovery-frame-contract-gate.md)
