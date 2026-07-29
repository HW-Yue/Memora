# 语义记录模型

状态：基本方向已形成；字段系统和关系类型仍未确认。

## AI 自定义 Schema

业务数据库、表和字段由 AI 根据领域自主设计。例如项目、个人、技术栈和书籍可以采用不同表结构。

引擎只强制维护系统信封：

```text
row_id, database_id, table_id, revision,
created_txn, updated_txn, valid_from, valid_to,
previous_version, status, actor, source, checksum
```

AI 可以演化业务 Schema，但不能破坏系统字段和版本链。

`created_txn/updated_txn` 表示数据库何时知道这件事，`valid_from/valid_to` 表示它在现实中何时有效，两套时间不能混用。`status` 至少区分 active、disputed、superseded 和 deleted；默认查询不能把过期版本伪装成当前事实。

## 语义记录

一条记录应当是可以独立理解和精确修改的完整认知模块，而不是按固定长度截断的原文 chunk。

候选预算：

- 目标约 800 个中文字；
- 约 300～1200 字为软范围；
- 一个主要主题；
- 标题和正文自解释；
- 可以独立建立关系和 revision。

800 字是写作预算，不是磁盘 Page 大小，也不是强制截断点。

## 关系

关系必须结构化保存，不能只隐含在正文：

```text
source_id, relation_type, target_id,
description, revision, status
```

AI 决定 `depends_on`、`contradicts`、`part_of` 等关系语义；引擎负责正反向索引、引用完整性和 MVCC。

## 修改能力

记录需要支持：

- INSERT、UPDATE、逻辑 DELETE；
- revise、supersede；
- merge、split；
- move、retype、rename；
- relation add/remove；
- 历史查询和补偿式撤销。

## 未决问题

- 系统是否需要统一的关系表，还是允许数据库自行建模？
- 是否需要限制每条记录最多关系数？
- 800 字预算按字符还是按估算 token 执行？
- AI 如何识别一条记录混入多个主题并主动 split？
- 默认查询如何呈现 disputed 和 superseded 记录？
- Source Receipt 应挂在 Row、字段，还是具体 claim 上？

## 关联

- [AI 自主权与约束](../agent/autonomy.md)
- [语义路由](../query/semantic-routing.md)
- [Wiki 导出](../export/obsidian-wiki.md)
- [自描述 Data Dictionary](./self-describing-data-dictionary.md)
