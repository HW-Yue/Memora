# MSQL Route Trace Read v1

状态：F114 已完成并验收；2026-08-01 冻结 v1 receipt、读取、retention 与隐私边界。

## 目的

Route Trace 是可清理的宿主观察收据，用来解释一次 AI Route 导航看到了哪些稳定 ID、
选择了哪一支、何时回退、打开了哪些 locator，以及各步耗时/预算。它不是业务事实、
Change Log、Security Audit、prompt log 或隐藏推理。

## Timeline

```sql
SHOW ROUTE TRACES [IN DATABASE work] [AFTER TRACE_SEQUENCE :sequence]
  [CURSOR :cursor] LIMIT :limit;
```

- `LIMIT` 必填，范围 1–256；AFTER 与 CURSOR 互斥；
- 首屏固定当前 high-water 和 retention epoch；新 trace 不混入旧 cursor；
- summary 只返回 trace ID/sequence、时间、actor、Database/Table ID、status、step count、
  expiry 和 checksum，不返回 steps；
- retention 清理改变 epoch，旧 cursor 返回 `revision_conflict`，不能静默漏页。

## Trace steps

```sql
SHOW ROUTE TRACE :trace_id [IN DATABASE work] [CURSOR :cursor] LIMIT :limit;
```

按 ordinal 分页返回 operation、parent RouteID、可见候选 RouteID、selected RouteID、
RowID/revision locator、稳定 result code、耗时和剩余预算。cursor 绑定 trace checksum、
Database scope 与 offset。

F122 起每个 step row 还明确返回 trace 自身的 `database_id/table_id`，让 stable-ID Admin
深链路可以验证 scope 后再生成 Route/Row 链接。空候选与 locator 按 non-nullable column
contract 确定编码为 `[]`，不使用 `null`。

## 授权与隐私

带有限 Database authorization 的调用必须显式 `IN DATABASE`；本地无 scope 管理会话可
读取全 Instance。记录由宿主显式提交结构化 receipt，因为引擎不能也不应猜 AI 选择；
receipt 永不接受问题正文、prompt、节点描述、Row values、模型输出、token 文本或推理。

## 边界

- trace 存入可删除的 auxiliary observation store，不进入 Database body、Change Log、
  Page generation、logical snapshot 或长期 system prompt；
- F114 提供 envelope、记录入口、retention prune 和读取协议，不做 Admin 页面；
- predictor provenance、模型 token/调用成本扩展留给 F124a–F124e 的独立 Review。

## 关联

- [Route Trace Store v1](../storage/route-trace-store-v1.md)
- [F114 开工与完成门](../archive/planning/f114-trace-read-protocol-gate.md)
