# 物理与语义索引

状态：职责边界已确认；B+ Tree 是必做的持久化主索引，Page、树和业务索引由
F81–F106 逐项 Review。F19–F23 的混合倒排主路径已撤销；ADR-0007 只允许可回退的
Route 候选预测器。

## 两套职责

引擎使用的物理索引：

- 主键/稳定 RowID 定位；
- 唯一键、字段精确值、范围和时间索引；
- 关系正反向索引；
- `row_id → Route memberships` 反向索引；
- B+ Tree（主索引已确认）、Posting Run、tombstone 等物理结构。

AI 使用的语义索引：

- Database 和 Table 的 purpose、边界与 Row 语义；
- 每个 Table 独立的 Router Tree；
- Route 节点的短描述、aliases 和稳定 ID；
- 叶子中的有限 `row_id + revision` locator。

语义树决定“AI 应沿哪个含义继续找”，物理索引决定“引擎怎样快速取得已明确
指定的键、范围、关系或 Route 节点”。候选预测器可以提示从哪里开始读，但不能
把物理分数变成事实权威或隐藏查询主路径。

## 永久边界

禁止：

- 为 Row 正文、文档 chunk、图片或事实建立权威向量副本；
- 让 cosine、点积、欧氏距离或隐藏融合分数直接返回答案；
- 把机械 N-gram 命中包装成语义相似度；
- 全库扫描后把正文交给模型挑选。

允许通过 MSQL 显式调用字面位置聚合或 Route-only Vector 候选。它们只能返回
Database/Table/Route ID、revision、来源和有界分数；AI 仍读取 Router 并 SQL 回表。
完整边界见 [ADR-0007](../decisions/0007-route-predictor-arsenal.md)。

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

Route vector 使用独立可重建 generation，绑定 embedding model/version、维度、
Route revision 和来源 digest；它不能进入 Row/Route 权威 snapshot。

新 generation 在当前版本继续查询时构建，经过结构、revision、覆盖和权限校验后
原子发布；旧读者释放后再回收旧版本。少量修改不得触发整表重建。

物理索引可以有各自 generation，但 manifest 不能让不同 snapshot 的 Row 与
Route 组合成一个虚假一致状态。

## 物理主索引

B+ Tree 持久化以下最小逻辑 key space：

```text
table_id + row_id → latest visible revision locator
row_id + commit_sequence/revision → immutable revision locator
table_id + row_id → ordered live/tombstone state
Catalog name/id → current Schema revision locator
```

精确 RowID Get 沿根到叶定位，Table cursor 沿叶链有序前进。内存 Catalog/Page Map
只作为缓存；重启从已提交 root/manifest 打开，不能全量扫描 Row Record 重建索引。
详见 [ADR-0005](../decisions/0005-btree-mandatory-primary-index.md)。

最小 MVCC 使用 immutable Row revision、commit marker 和 snapshot sequence；
不预设“最新 Record + 物理 Undo chain”。长期 History 仍独立保存语义 revision。
见 [ADR-0004](../decisions/0004-fast-row-directory-minimal-mvcc.md)。

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
- [ADR-0007：Router 权威，候选预测器可组合](../decisions/0007-route-predictor-arsenal.md)
