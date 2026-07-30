# 物理与语义索引

状态：职责边界已确认；原生物理布局尚未冻结。F19–F23 的混合倒排主路径已撤销。

## 两套职责

引擎使用的物理索引：

- 主键/稳定 RowID 定位；
- 唯一键、字段精确值、范围和时间索引；
- 关系正反向索引；
- `row_id → Route memberships` 反向索引；
- B+ Tree、Posting Run、tombstone 等候选物理结构。

AI 使用的语义索引：

- Database 和 Table 的 purpose、边界与 Row 语义；
- 每个 Table 独立的 Router Tree；
- Route 节点的短描述、aliases 和稳定 ID；
- 叶子中的有限 `row_id + revision` locator。

语义树决定“AI 应沿哪个含义继续找”，物理索引决定“引擎怎样快速取得已明确
指定的键、范围、关系或 Route 节点”。两者不能混为相似度评分系统。

## 永久禁止

语义发现、评测和兜底均禁止：

- Embedding 或向量数据库；
- dense/sparse/字符向量；
- cosine、点积、欧氏距离或等价距离排名；
- 把机械 N-gram 命中包装成语义相似度；
- 全库扫描后把正文交给模型挑选。

若保留传统全文或 N-gram 能力，它只能由用户/AI 通过明确的“字面查找”语句调用，
返回字面命中，不能自动接管 Router 失败，也不能作为语义主路径。

## Row 与 Route 原子性

稳定 `row_id` 是 Row 的永久逻辑身份。新增、修改、删除、split 和 merge 后：

- 当前 Record 与普通物理索引按同一事务可见；
- 所有旧 Route membership 通过反向索引立即失效；
- 新 membership 绑定新 Row revision 后原子启用；
- 暂无 AI 语义结果时标记 `pending_reindex`，不能继续暴露旧定位；
- History 保留旧 revision，Router 不复制正文。

## Generation

每个 Table 的 Router 支持：

1. 单 Row membership 增量替换；
2. 局部子树旁路重建；
3. Table generation 重建。

新 generation 在当前版本继续查询时构建，经过结构、revision、覆盖和权限校验后
原子发布；旧读者释放后再回收旧版本。少量修改不得触发整表重建。

物理索引可以有各自 generation，但 manifest 不能让不同 snapshot 的 Row 与
Route 组合成一个虚假一致状态。

## 物理主数据候选

物理 MVCC 使用“最新 Record + Undo version chain”：

```text
Clustered/Row Directory B+ Tree
  → 当前 Record 或稳定逻辑定位

Secondary Index
  → 稳定 row_id

Undo Log
  → 短期事务旧版本

History Store
  → 长期语义 revision
```

语义 Row 的字符预算属于 Schema/Column 约束；Page 的字节容量属于物理存储。
Row split 由 AI 按语义完成，Page split 由引擎自动完成，二者不能混淆。

## 未决问题

- 主目录叶子内联多少小字段；
- 内部 RowID 编码；
- 精确 Secondary Index 引用当前对象还是具体 revision；
- Router 节点、membership 和传统字面索引分别采用何种物理结构；
- manifest 怎样固定 Row/Route/关系的一致 snapshot。

## 关联

- [AI-native 产品宪章](../product/ai-native-product-charter.md)
- [Agent 语义目录索引](../query/semantic-routing.md)
- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
