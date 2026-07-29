# Memora

Memora 是由 AI 自主建模、通过版本化 MSQL 读写的本地个人数据库。

项目当前处于架构设计和端到端原型准备阶段。计划中的 `memora` CLI 会直接承载数据库 Instance、统一 MSQL 执行引擎和内置 Loop Agent：

```text
memora                进入交互式 Agent/MSQL 循环
memora --stdio        为外部 Agent 提供长驻 JSONL 会话
memora ask <intent>   使用内置 Agent 查询
memora exec <msql>    直接执行 MSQL
```

内置 Agent 与外部调用方必须经过同一套 MSQL Parser、Policy、事务和执行器；Agent 不能直接操作 Page、索引或日志。

当前设计入口见 [`docs/README.md`](./docs/README.md)。
