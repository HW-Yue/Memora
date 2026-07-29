# 市场调研入口

更新时间：2026-07-29。只记录当前官方文档可复核的能力；厂商 benchmark 未独立复现。

## 分类

- [Agent 记忆与个人知识产品](./market-memory-systems.md) — Mem0、Letta、Graphiti、Basic Memory、Memvid 等；
- [AI 数据库与检索基础设施](./market-databases.md) — seekdb、HelixDB、LanceDB、Chroma、Qdrant 等；
- [市场空白与 Memora 定位](./market-positioning.md) — 能借鉴什么、不能复制什么、真正差异在哪里。

## 总判断

市场已经分别验证了向量记忆、时间知识图、Markdown+MCP、单文件便携和数据库级版本分支，但尚未看到一个成熟产品同时做到：

```text
AI 自主 Database/Table/Column
+ SQL 精确读写
+ 短小自解释 Router
+ 语义记录可持续修订
+ 上下文预算是一等约束
+ 不依赖外部 Embedding API
+ 本地单可执行文件与 Wiki 导出
```

这是 Memora 的机会，也是必须用实验而非口号证明的组合假设。

## 名称风险

`Memora` 已被 [agentic-box/memora](https://github.com/agentic-box/memora) 用于一个面向 Claude/Codex 的 MCP 记忆项目；[MatrixOrigin Memoria](https://github.com/matrixorigin/Memoria) 名称和领域也高度接近。当前仓库名可以作为代号，但公开发布名属于 P0 决策。

完整早期调研保存在 [归档](../archive/AI_NATIVE_PERSONAL_DATABASE_RESEARCH_2026-07-29.md)，日常设计不依赖该长文档。
