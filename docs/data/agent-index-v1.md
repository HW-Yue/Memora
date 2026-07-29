# Agent Inverted Index v1

状态：F19a/F19b 已冻结完整词项快照、posting、预算与 Row/MSQL 原子接入。

## 输入契约

Agent 为一条完整语义 Row 提交 `index_terms: string[]`。词项可由任意业务字段综合产生，不记录来源 Column，也不接受增删 diff。

每次提交都是对应 Row revision 的完整快照。引擎执行：

- 校验 UTF-8，折叠首尾和连续空白；
- Unicode 小写化；
- 规范化后去重并按字节稳定排序；
- 单词项最多 128 个字符，禁止静默截断。

启动写作目标为 24 个词项，启动硬上限为 64 个。目标值用于 Agent 生成提示，不是低于硬上限的第二个错误门；超过硬上限返回稳定 constraint error。两项都由 Service 配置注入，为后续 Database 配置持久化保留边界。

## 持久化

每个 Row 保存版本化完整快照：

```text
database_id, table_id, row_id, revision
source = agent
state = active | invalid
terms[]
```

正向 posting 为：

```text
database_id + normalized_term
  → table_id + row_id + current_revision + source
```

反向快照和正向 posting 在同一个 caller-owned Store transaction 内替换。新 revision 先移除旧快照全部 posting，再写入新快照全部 posting；回滚不能暴露半套索引。历史快照按 revision 留存，但查询只读取当前 posting。

逻辑失效写入空的 `invalid` 新 revision，并移除旧 posting。相同或更旧 revision 的提交返回 revision conflict，防止迟到结果覆盖当前索引。

## 查询边界

F19 的 lookup 是精确规范化词项到 Row locator 的有界查询，最多返回 1000 个 posting。posting 明确标记 `source=agent`，只用于候选发现，不返回 Row 正文；调用方必须用 SELECT 回表。

`INSERT`、`UPDATE` 和 `RESTORE` 可在 MSQL mutation options 中携带非 nil `index_terms` 完整快照。空数组表示“本 revision 明确没有 Agent 词项”，字段缺失表示本次没有提供新快照。Row 当前记录、History 和 posting 在同一事务中提交；DELETE 总是写入 invalid 快照并移除活跃 posting。

普通 UPDATE 缺少 `index_terms` 时的 durable `pending_reindex` 状态和后台重建属于 F24；在 F24 完成前，Agent 维护链路必须随语义修改提供完整快照。

F20 的机械 posting 使用独立来源和结构。F21 才负责两路归一化与融合评分。

## 关联

- [物理与检索索引](../storage/indexing.md)
- [语义记录模型](./semantic-records.md)
- [MSQL](../query/msql.md)
