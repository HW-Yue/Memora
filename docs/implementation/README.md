# 当前实现

本目录只描述**现在真的是怎么实现的**。一个问题一个文件，不写未来计划（计划在
[`../planning/`](../planning/README.md)），也不写还没解决的问题（问题在 [`../issue/`](../issue/README.md)）。

状态约定：文件顶部一行 `状态：` 说明它对应哪一批实现，`已实现` / `已冻结` 都意味着代码里有对应物。

## 现行内核

| 子目录 | 内容 | 入口 |
|---|---|---|
| [`query/`](./query/) | MSQL 语言、Catalog DDL、读取面、语义路由、四条检索路、jev 走树 | [msql.md](./query/msql.md) · [semantic-routing.md](./query/semantic-routing.md) |
| [`data/`](./data/) | Catalog 对象、逻辑类型、语义记录、自描述数据字典 | [catalog-v1.md](./data/catalog-v1.md) |
| [`storage/`](./storage/) | SQLite 基座之上的存储层现状 | [storage/README.md](./storage/README.md) |
| [`agent/`](./agent/) | Skill 契约、写入契约、MCP 适配、Agent/引擎分界 | [canonical-skill-v1.md](./agent/canonical-skill-v1.md) · [skill-write-v1.md](./agent/skill-write-v1.md) |

## 对外面（surface）

| 文件 | 内容 |
|---|---|
| [surfaces/cli-database-workflow.md](./surfaces/cli-database-workflow.md) | CLI 的库工作流 |
| [surfaces/ipc-protocol.md](./surfaces/ipc-protocol.md) | daemon IPC 协议 |
| [surfaces/go-sdk-v1.md](./surfaces/go-sdk-v1.md) | Go SDK |
| [surfaces/macos-launch-agent-v1.md](./surfaces/macos-launch-agent-v1.md) | macOS LaunchAgent |
| [surfaces/process-configuration.md](./surfaces/process-configuration.md) | 进程与配置 |
| [surfaces/testing.md](./surfaces/testing.md) | 测试与门禁 |

## 已交付设计（做完的规划搬到这里留档）

- [design/recall-rrf.md](./design/recall-rrf.md) · [design/recall-chinese-queries.md](./design/recall-chinese-queries.md)
- [design/vector-rekey.md](./design/vector-rekey.md) · [design/whole-layer-read.md](./design/whole-layer-read.md)
- [design/route-purpose-contract.md](./design/route-purpose-contract.md) · [design/autonomous-write.md](./design/autonomous-write.md)
- [design/admin-display-slots.md](./design/admin-display-slots.md) · [design/admin-canvas-gestures.md](./design/admin-canvas-gestures.md)
  · [design/admin-canvas-performance.md](./design/admin-canvas-performance.md)

## 不在本目录的两层

- **产品方向与契约**（最高参考规范）：[`../product/`](../product/) —— 冲突时以它为准。
- **规范级结论**（ADR）：[`../decisions/`](../decisions/)；运行中的判断在 [`../decisions.md`](../decisions.md)。
