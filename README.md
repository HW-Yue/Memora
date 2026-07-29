# Memora

Memora 是由 AI 自主建模、通过版本化 MSQL 读写的本地个人数据库。

项目当前处于架构设计和端到端原型准备阶段。计划中的本地 `memora` daemon 长期承载数据库 Instance、统一 MSQL 执行引擎和文件 Page 的 Buffer Pool；Codex/Claude Code 按 Memora Skill 通过 CLI 连接 daemon：

```text
memora --stdio        为外部 Agent 提供长驻 JSONL 会话
memora exec <msql>    直接执行 MSQL
memora daemon         管理本地常驻服务
```

所有 Agent 操作必须经过同一套 MSQL Parser、Policy、事务和执行器；Agent 不能直接操作 Page、索引或日志。自带模型的 `memora ask` 属于 v0 发布后的可选评估，不阻塞 Skill-first 产品。

当前设计入口见 [`docs/README.md`](./docs/README.md)。
