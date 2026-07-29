# 语义记录模型

状态：基本方向已形成；字段系统和关系类型仍未确认。

## AI 自定义 Schema

业务数据库、表和字段由 AI 根据领域自主设计。例如项目、个人、技术栈和书籍可以采用不同表结构。

引擎只强制维护系统信封：

```text
row_id, database_id, table_id, revision,
created_txn, updated_txn, valid_from, valid_to,
previous_version, row_state, actor, source, checksum
```

AI 可以演化业务 Schema，但不能破坏系统字段和版本链。

`created_txn/updated_txn` 表示数据库何时知道这件事，`valid_from/valid_to` 表示它在现实中何时有效，两套时间不能混用。`row_state` 只表达 live/deleted 等存储生命周期，不判断内容真假。引擎不内置 `candidate`、`disputed` 或 `superseded` 业务状态；需要这类领域概念时，由 AI 在业务 Schema 中定义并通过普通 SQL 维护。

## 语义记录

一条记录应当是可以独立理解和精确修改的完整认知模块，而不是按固定长度截断的原文 chunk。

内容原则：

- 目标约 800 个中文字；
- 每个文本 Column 各自声明最大字符数，启动默认值为 1200；
- 一个主要主题；
- 标题和正文自解释；
- 可以独立建立关系和 revision。

800 字是语义模块的启动写作目标，不是固定 Row 上限，也不是磁盘 Page 大小。文本字段超过其 Column 当前配置时，引擎必须返回结构化错误；Agent 根据语义选择切分数据、改用合适字段类型或提交 Schema 调整，禁止静默截断。Column 默认 1200 字符和写作目标都可通过 MSQL、Policy 与 revision 演化。

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
- 按稳定 `row_id` 精确 SELECT、UPDATE 和 DELETE；
- revise、supersede；
- merge、split；
- move、retype、rename；
- relation add/remove；
- 历史查询和补偿式撤销。

所有修改都必须通过 MSQL/SQL 进入统一事务执行器。`row_id` 永不因正文、Schema、Router 归属或索引重建而改变；UPDATE 创建新语义 revision，逻辑 DELETE 将当前状态改为 deleted 并默认保留历史。物理清除使用单独的高风险 PURGE。

Row 修改或删除时，引擎必须在同一事务中更新当前 Record、物理索引、机械 posting、关系引用和 Binlog。Agent 维护的完整 `index_terms` 与 Router 叶子归属也必须通过声明式 MSQL 替换或失效，不能留下指向旧内容的活跃引用。

## 未决问题

- 系统是否需要统一的关系表，还是允许数据库自行建模？
- 是否需要限制每条记录最多关系数？
- AI 如何识别一条记录混入多个主题并主动 split？
- 默认查询怎样结合当前 revision 和现实有效时间排除已过期内容？
- Source Receipt 应挂在 Row、字段，还是具体 claim 上？

## 关联

- [AI 自主权与约束](../agent/autonomy.md)
- [语义路由](../query/semantic-routing.md)
- [Wiki 导出](../export/obsidian-wiki.md)
- [自描述 Data Dictionary](./self-describing-data-dictionary.md)
