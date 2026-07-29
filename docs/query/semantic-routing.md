# 语义路由

状态：三路检索方向已形成；Router 数据模型未确认。

## 目标

Agent 不看内部 ID、Page 或 offset，而是阅读很短的语义目录，迅速知道下一步进入哪个数据库、路径和表。

## Router Page

Router 只包含：

- 当前逻辑路径和一句话用途；
- 最多约 8～12 个子分支；
- 每个子分支一句话 scope；
- 相关表；
- 可选查询提示；
- Schema/Route revision。

候选预算为 300～500 个中文字，硬上限约 800 字。Router 不返回完整业务记录。

## 三路检索

```text
Semantic Router：理解性导航
Inverted Index：BM25/N-gram 全局召回，防止漏检
Relation Graph：扩展结构和语义邻居
```

最终由 SQL 选择和读取语义记录。

## 语义分裂

物理 Page 满时引擎自动 split。Router 超过预算时，引擎只能报告 overflow；怎样按语义重新分组必须由 AI 决定并通过事务修改。

## 内部身份与外部路径

- 内部使用稳定 route/object ID；
- Agent 使用 `/project/memora/indexing` 等语义路径；
- 同一导航会话可使用 `@1` 等短句柄；
- 重命名路径不能改变内部身份；
- 旧路径可作为别名或 redirect。

## 未决问题

- Router 是系统表、普通 AI 表还是独立系统对象？
- Router 如何自动发现内容变化并提示需要重组？
- 路由与倒排结果如何合并评分？
- 根目录数据库很多时如何避免 `SHOW DATABASES` 自身变长？
- Router 路径错误时如何保证仍能通过全文检索找到记录？

## 关联

- [MSQL](./msql.md)
- [无向量检索质量链路](./retrieval-quality.md)
- [上下文生命周期](./context-lifecycle.md)
- [物理与检索索引](../storage/indexing.md)
