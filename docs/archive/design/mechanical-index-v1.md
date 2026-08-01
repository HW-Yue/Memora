# Mechanical Inverted Index v1

状态：F20a/F20b 的历史 Row 索引规格；当前语义查询不再使用。F124b 是独立的
Route/Catalog 位置预测器，不读取或复用本规格的 Row posting。

## Tokenizer v1

机械索引只处理完整 Row 的文本字段，不理解业务语义：

- Unicode letter/digit 连续段生成小写完整词；
- 长度至少 3 的 letter/digit 词同时生成连续 trigram；
- 连续汉字生成 bigram，单个汉字保留 singleton；
- 标点、符号和空白是边界；
- 多字段按 Catalog Column 顺序输入。

输出按首次出现顺序去重。Tokenizer 版本写进每个快照，未来规则变化必须走显式 rebuild，不能在读取时偷偷改变旧 posting。

## 空间预算

启动配置每 Row 最多处理 20,000 个字符、最多保留 256 个唯一机械词项。达到任一预算后停止新增派生词并写入 `truncated=true`，但不截断或修改原始 Row。预算可注入，查询和诊断必须能区分完整快照与预算截断快照。

## 存储与来源隔离

机械快照包含稳定 Row locator、revision、`source=mechanical`、state、tokenizer version、terms 和 truncated。正向 posting 与反向快照在同一 caller-owned transaction 内替换。

Agent 与 mechanical 使用独立 bucket、独立 source 和独立生命周期。禁用、失效或重建机械索引不得修改 Agent posting。

状态：

- `active`：当前 revision 的机械词项；
- `disabled`：Database Policy 关闭机械索引，不生成 posting；
- `invalid`：Row 已删除或快照显式失效。

普通 replace 要求更高 Row revision。同 revision 的 `RebuildIn` 被允许，用于 tokenizer/config 重建；它先移除旧机械 posting，再原子发布新快照。F25 再把大规模旁路构建接入 generation manifest。

Row transaction 按稳定 Catalog Column 顺序读取所有当前 TEXT 值；INTEGER、BOOLEAN、TIMESTAMP、RELATION_ID 和 NULL 不进入 tokenizer。INSERT、UPDATE 和 live RESTORE 自动完整替换机械快照，DELETE 和 deleted RESTORE 自动失效；调用方不提交机械词项，也不能直接操作 posting。

单 Row `RebuildMechanicalIndex` 在当前 revision 原子重建，供诊断和后续 REINDEX Planner 复用。显式 Batch rollback 同时撤销 Row、History、Agent posting 和机械 posting。

## 查询边界

精确机械词项 lookup 最多返回 1000 个 `source=mechanical` Row locator，不返回正文。F21 负责和 Agent posting 分别归一化后融合，最终仍需 SELECT 回表。

## 关联

- [Agent Inverted Index v1](./agent-index-v1.md)
- [物理与检索索引](../../storage/indexing.md)
- [MSQL](../../query/msql.md)
