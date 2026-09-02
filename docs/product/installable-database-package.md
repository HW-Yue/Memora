# 可安装的独立语义数据库

状态：产品形态已确认；F44 已冻结 v1 包格式。

> **实现已于 2026-09-02 全部删除**（fork／merge／upgrade 在 `27f8164`，
> 包与安装在本轮）。删的理由是整条链端到端从来没通过：实现只接受已死的
> legacy `store.Store` 栈，生产走 native 栈时管理器恒为 nil。
> **本文降为设计记录**，重新开发时以它与
> [Database Package v1](./database-package-v1.md) 为规格。

## 产品定义

Memora 的基本可分发对象不是聊天记录、Markdown 目录或向量索引，而是一个自描述的逻辑 Database。它可以从个人 Instance 导出，交给另一个人一键安装，并由 `memora` CLI 直接启动问答。

```text
个人全局 Instance
  → 多个有边界的逻辑 Database
  → 导出某个 Database 为可携带包
  → 另一台机器校验并安装
  → 陌生 Agent 或用户立即查询
```

“全局记忆”表示默认问答入口能在 Policy 允许的全部 Database 中发现和路由，不表示把所有项目打平到一个无边界 collection。

## 三种开箱即用方式

以下 CLI 已实现；每个命令都转换为对应的 MSQL 管理语句，再经过统一 Parser 和执行器：

```text
Codex/Claude + Memora Skill    进入当前 Instance 的全局问答
memora exec <msql>             执行 Skill 生成的确定性操作

memora pack work_x --by Alice -o /absolute/exports/work_x.memora-db
memora install work_x.memora-db --trusted
memora open work_x.memora-db   校验并只读审阅 manifest
```

对应的 MSQL 是 `PACK DATABASE ... BY :author`、`INSTALL PACKAGE :package TRUSTED` 和
`OPEN PACKAGE :package READ ONLY`；包字节始终参数绑定，不使用路径或通用 `CALL` 包装。

- `install` 把一个库纳入本地 Instance，之后可参与全局路由；
- `open` 不导入个人 Instance，适合审阅、演示和一次性问答；
- `pack`、`install` 和 `open` 不得直接调用绕过 MSQL 的包管理接口；
- 外部 Agent 仍可使用 `--stdio`、Skill 或可选 MCP adapter；
- v0 由 Codex/Claude Code Skill 提供 AI；数据库包本身不绑定某个模型，也不携带 Provider 凭据。未来可选的内置 Runtime 另行评估。

## 包的逻辑内容

最小包必须包含：

- manifest：格式版本、Database 身份、名称、用途、作者声明和兼容范围；
- Data Dictionary：Table、Column、关系、别名、约束和短描述；
- Database 级可演化配置及其 revision，包括 Search Weight、词项、Router 和 Context Pack 预算；
- 当前可见语义 Row、revision、关系和必要 Source Receipt；
- Database Router 和供冷启动发现的短摘要；
- 完整性清单与内容哈希。

倒排索引等派生状态可以随包携带以加速打开，但安装方必须能丢弃并确定性重建。模型 API Key、Provider 凭据、Query Workspace、LRU、未提交事务、其他 Database 数据和宿主聊天记录不得进入包。

物理存储中，Database 的 `data/` 与 `history/` 属于权威内容，`indexes/router/` 和 `indexes/inverted/` 属于可选派生 generation。打包不能因省略派生索引而丢失任何 Row、revision 或关系。

是否携带完整 revision 历史是待验证策略；至少要保留当前 snapshot 的 revision 身份和来源，使安装后修改仍可审计。

## 安装语义与安全边界

安装前必须校验格式版本、哈希、Database 身份、对象数量和兼容性。发生同 ID、同名异义或已有旧版本时，不静默覆盖，由引擎给出 upgrade、fork、rename 或拒绝方案。

数据库包是数据，不是插件：

- 不携带可执行代码、安装 hook 或自动运行脚本；
- 数据中的文本不能获得 system prompt 或工具指令权限；
- 包声明的模型外发需求、跨库访问和可写范围必须由本地 Policy 决定；
- 未信任包默认只读打开，写入、跨库关系和外部模型访问需要显式授权；
- 回答必须能返回 Database、Table、Row、revision 等来源定位。

## 全局问答链路

```text
自然语言问题
→ Instance 级 Database Router 召回候选库
→ 读取候选库的短 Data Dictionary / Route
→ 对一个或少量库执行 MSQL
→ 必要时做受预算约束的跨库关系扩展
→ 返回答案、来源定位和不确定性
```

默认目标是“用户不需要记住内容在哪个项目”，但引擎仍保留项目、隐私、导出和删除边界。问答不能用跨库便利性绕过 Policy，也不能在证据不足时伪造“记得”。

## 第一阶段验收

- 一个新 Agent 不读旧聊天即可安装包并回答基准问题；
- 同一包在新机器重建派生索引后得到等价查询结果；
- 全局问题能选中正确 Database，且不会把同名不同项目事实串库；
- 指定 Database 的问题不会读取范围外内容；
- 包冲突、损坏、版本不兼容和不可信内容都有明确失败结果；
- 安装、直接打开和全局问答都由本地 daemon 统一执行。

## 后续候选

- `open` 的一次性数据问答 runtime；F44 只提供无副作用 manifest/完整性审阅；
- revision 历史、Undo 信息和附件型 Source Receipt 的携带范围；
- 商业数据库包的授权、撤销和离线使用模型。

## 关联

- [AI-native 产品契约](./ai-native-contract.md)
- [Database Package v1](./database-package-v1.md)
- [可选内置 Agent Runtime](../agent/embedded-agent-runtime.md)
- [Instance、Database 与 Table](../storage/instance-database-table.md)
