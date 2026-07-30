# 可选内置 Agent Runtime

状态：F43 已决定 v0 defer；仅满足 [ADR-0002](../decisions/0002-defer-embedded-agent.md) 的重新开启条件后评估。

## 定位

v0 由 Codex/Claude Code 按 Canonical Skill 生成 MSQL，并调用本地 daemon。只有 Skill-first 产品验证后仍存在明确的独立使用需求，才考虑让 Memora 自带模型 Provider 和 `memora ask`。本文保留候选 Runtime 的边界，不能作为 v0 依赖。

内置 Agent 负责语义决策，引擎仍负责确定性执行。二者可以随同一个本地产品发布，但必须保持模块边界。

## 两种入口，一个执行核心

```text
自然语言
  → memora ask
  → Agent Loop
  → MSQL Request ─┐
                  ├→ Lexer/Parser → Binder → Policy → Planner
外部 Agent / CLI  │                    → Transaction → Executor
  → memora exec   │                    → JSON Envelope
  → MSQL Request ─┘
```

- `memora ask` 接收自然语言，由内置 loop 发现 Schema、生成 MSQL、读取结构化结果并继续推理；
- `memora exec`、SDK 和可选 MCP adapter 直接提交 MSQL；
- 两条路径最终调用同一个版本化 `ExecuteMSQL` 接口；
- 内置 Agent 不能直接构造绕过 Parser 的内部写操作，也不能访问 Page、索引或日志文件。

“统一入口”指统一进入 MSQL 执行核心，不把自然语言并入 MSQL Grammar。自然语言始终先由 Agent 转换为明确的 MSQL。

## 统一请求信封

内部 loop 与外部调用方使用同一种逻辑请求：

```json
{
  "protocol": "msql/1",
  "actor": {"kind": "embedded_agent", "session": "..."},
  "statement": "SELECT ... LIMIT 5",
  "params": {},
  "mode": "execute",
  "budget": {"rows": 5, "chars": 2400},
  "scope": ["project_memora"]
}
```

actor、权限、预算和审批状态可以不同，但语法解析、AST、类型检查、Policy 和事务语义不能分叉。

## Agent Loop

```text
接收意图
→ 恢复紧凑 Query Workspace
→ 让模型生成 query_terms 并选择下一条 MSQL
→ 通过 ExecuteMSQL 执行
→ 读取稳定 JSON 结果或错误
→ 继续、修正、请求批准或结束
→ 返回回答 / Context Pack / Mutation Receipt
→ 保存可重建的 checkpoint
```

循环必须有确定性的最大步数、token、时间、返回字符、扫描行数和修改行数。达到预算时显式返回 `truncated` 或 `budget_exhausted`，不能无限自治。

查询采用两阶段分工。内置 Runtime 先以隔离的索引发现 Sub-agent 逐层发现 Database、Route 和 Table，并融合倒排与关系信号，只返回候选数据项定位；主 Agent 再根据定位生成 MSQL `SELECT` 回表读取真实 Row。发现结果不能包含正文，也不能直接作为最终答案。

执行 `MATCH(...) AGAINST(...)` 时，Query Agent 按 Query Skill 为当前意图输出去重后的 `query_terms: string[]`，可以补充原问题未出现的同义词、旧名称、缩写和跨语言别名，用于 Agent 词项通道；引擎同时从原始问题生成机械词项，用于机械通道。两路结果按目标 Database 的 Search Weight Profile 融合。

Skill 规定生成行为和格式，但不是唯一约束层。Runtime 校验 JSON Schema、数量和长度，Data Dictionary 提供已知 alias，MSQL 负责正式传递，Policy 强制查询预算。`query_terms` 启动预算为 12 个、启动 Policy 上限为 32 个；两者存于 Database 配置，建库后是否允许 AI 调优留到配置生命周期设计。

## 一个 Runtime，多种能力配置

不再要求 Query Agent 和 Mutation Agent 是两个不同宿主进程。一个 loop 根据请求获得能力配置：

- read profile：只允许 SHOW、DESCRIBE、ROUTE、SELECT、EXPLAIN；
- write profile：允许可逆局部修改，但仍受 revision、Schema version 和影响行数约束；
- schema profile：先生成影响计划并等待 Policy 或用户批准；
- destructive profile：PURGE、清历史和放宽隐私必须显式授权。

能力由引擎 Policy 强制，不依赖 prompt 自律。

## 本地 daemon 与上下文

本地 `memora` daemon 长期打开一个 Instance，并在同一进程中承载存储引擎、Agent Runtime、Provider 连接、文件 Page 的 Buffer Pool 和按 scope 隔离的 Query Workspace。CLI、`--stdio` bridge、SDK 和可选 MCP adapter 都是 daemon 的本地客户端，不能各自创建互不一致的缓存与事务域。

直接运行 `memora` 时默认进入前台交互循环，直到用户 `exit`，但数据库和缓存继续由 daemon 持有。外部 Agent 可以通过 `memora --stdio` 使用 JSONL bridge；单次 `ask`/`exec` 命令连接 daemon、执行并退出，不重新打开 Instance。

Buffer Pool 按 Page 的访问热度保留最近读取的数据和索引，Agent 上一次查询访问的 Page 因而通常仍在内存，但不会获得语义优先级。Query Workspace 只保存当前 loop 的有界状态，模型原始上下文不能无限增长；它与物理 Page 缓存分别计费和失效。

## 模型与密钥

- 用户可以配置云模型或本地模型；
- API Key 进入操作系统密钥存储或进程环境，不进入用户数据库、prompt、日志或 Wiki 导出；
- 每个 Database/Policy 可以限制是否允许把内容发送给外部模型；
- Provider 不可用时，`memora exec` 的确定性 MSQL 能力仍应可用。

## 第一版 CLI 形态

```text
memora                       进入交互式 Agent/MSQL 循环
memora --stdio               启动供外部调用方持有的 JSONL 会话
memora ask <intent>          使用内置 Agent Loop
memora exec <statement>      直接执行 MSQL
memora daemon ...            启动、停止和检查本地 daemon
memora config model ...      配置模型，密钥单独安全保存
```

## 若重新开启仍需冻结

- Provider 抽象和第一版支持范围；
- daemon 的启动、退出、崩溃恢复、本地 IPC 和客户端会话协议；
- Query Workspace 的持久化位置、TTL 和最大尺寸；
- loop 的默认步数、token、时间和费用预算；
- 写入审批如何同时支持交互 CLI 和非交互调用；
- 模型输出采用工具调用还是严格 JSON；
- `ask` 最终返回人类回答、Context Pack，还是由调用方选择；
- 同一 loop 是否处理资料吸收，还是使用独立任务类型。

## 关联

- [MSQL](../query/msql.md)
- [上下文生命周期](../query/context-lifecycle.md)
- [AI 自主权与约束](./autonomy.md)
