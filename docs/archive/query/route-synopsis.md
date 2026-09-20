# 中间 Route Synopsis

状态：F77 已实现。

## 产品判断

中间语义节点可以保存可选 `synopsis`，但它是版本化 Route 元数据，不是普通业务
Row，也不是模型答案缓存。

模型预训练包含通用世界知识，却不知道用户私有数据库此刻有哪些分支、哪些 Row
已经拆分或合并、当前 scope/anti-scope 怎样划分。因此 synopsis 有价值；重复通用
知识、保存事实答案或机械汇总正文则没有价值。

## 两层预算

- `purpose`：默认逐层导航始终返回的一句话选择标签；
- `synopsis`：0–1000 字符，建议只给含混的 branch 写约 300–1000 字；
- `SHOW ROUTES` 不返回 synopsis；
- AI 只有无法仅靠相邻 purpose 稳定选择时，才执行
  `DESCRIBE ROUTE :route_id`；
- synopsis 不进入长期 system prompt，只进入当前查询的 Route Frame。

这样常见路径仍然快，困难路径才支付额外一次读取和上下文成本。

## 应写与不应写

synopsis 应描述：

- 当前私有子树覆盖什么、明确不覆盖什么；
- 子分支的选择边界和容易混淆之处；
- 拆分、合并或迁移后仍有效的导航提示。

synopsis 不应：

- 充当事实答案来源，绕过 `SELECT` 回表；
- 复制子树全部正文或生成机械 chunk 摘要；
- 保存模型已经知道的通用定义；
- 包含 embedding、向量、相似度分数或隐藏候选排名。

## 更新规则

```sql
CREATE ROUTE UNDER :parent NAME :name KIND :kind
  PURPOSE :purpose SYNOPSIS :synopsis;
DESCRIBE ROUTE :route_id;
ALTER ROUTE :route_id SET SYNOPSIS :synopsis;
```

独立更新必须带 expected Route revision。SPLIT/MERGE 改变子树边界时，
`route_updates` 可在同一原生事务中更新 purpose 和 synopsis；失败时 Row、
History、Relation、Route 与 membership 一起回滚。

