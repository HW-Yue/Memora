# 内置 Agent Runtime 与统一执行入口

状态：顶层方向已确认；模型协议、循环预算和审批交互仍待原型验证。

## 定位

Memora 内置可独立运行的 Agent Runtime。用户配置自己的模型提供方和 API Key 后，可以直接通过自然语言使用数据库，不依赖 Codex、Claude Code 或其他宿主提供 sub-agent。

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
→ 让模型选择下一条 MSQL
→ 通过 ExecuteMSQL 执行
→ 读取稳定 JSON 结果或错误
→ 继续、修正、请求批准或结束
→ 返回回答 / Context Pack / Mutation Receipt
→ 保存可重建的 checkpoint
```

循环必须有确定性的最大步数、token、时间、返回字符、扫描行数和修改行数。达到预算时显式返回 `truncated` 或 `budget_exhausted`，不能无限自治。

## 一个 Runtime，多种能力配置

不再要求 Query Agent 和 Mutation Agent 是两个不同宿主进程。一个 loop 根据请求获得能力配置：

- read profile：只允许 SHOW、DESCRIBE、ROUTE、SELECT、EXPLAIN；
- write profile：允许可逆局部修改，但仍受 revision、Schema version 和影响行数约束；
- schema profile：先生成影响计划并等待 Policy 或用户批准；
- destructive profile：PURGE、清历史和放宽隐私必须显式授权。

能力由引擎 Policy 强制，不依赖 prompt 自律。

## CLI 进程与上下文

`memora` CLI 进程直接打开一个 Instance，并在同一进程中承载存储引擎、Agent Runtime、Buffer Pool、LRU、Provider 连接和按 scope 隔离的 Query Workspace。第一阶段不使用后台 daemon 或本地 socket。

直接运行 `memora` 时默认进入前台交互循环，直到用户 `exit`。外部 Agent 可以启动 `memora --stdio`，通过 stdin/stdout 的 JSONL 协议持有同一个长驻子进程。单次 `ask`/`exec` 命令则在当前进程打开 Instance、执行并退出。

长驻 CLI 会话中的逻辑 Agent 可以持续存在，但模型原始上下文不能无限增长。持久化内容应是 Database/Route、版本指纹、最近 Row 定位、未完成计划和紧凑 checkpoint；进程退出后由 Runtime 重建最小上下文。

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
memora config model ...      配置模型，密钥单独安全保存
```

## 尚未确认

- Provider 抽象和第一版支持范围；
- 交互 CLI 和 `--stdio` 的会话、退出及崩溃恢复协议；
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
