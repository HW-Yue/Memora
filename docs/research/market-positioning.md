# 市场空白与 Memora 定位

状态：调研后的产品判断，必须由原型和 benchmark 验证。

## 一句话定位

Memora 是由 AI 自主建模和维护、由 Agent 通过 SQL 精确读写、能够被陌生 Agent 低成本接管的本地个人数据库。

它的产品交付形态进一步明确为：一个全局本地 Instance 提供跨项目问答，每个逻辑 Database 又可单独打包、安装或直接打开问答。

## 市场空白

现有产品通常只覆盖以下一部分：

```text
Memory framework：会提取、召回，但数据模型和事务能力弱
Vector database：检索强，但不知道什么值得保存
Graph memory：关系和时间强，但精细表格修改和迁移弱
Markdown memory：人可读，但长期结构和同步成本高
Traditional SQL：修改和事务强，但不面向 Agent 上下文设计
Portable memory file：容易复制和直接 ask，但通常缺少自主关系 Schema 和精确事务修改
```

Memora 的机会是把 AI 的语义自治与数据库的确定性约束放在同一个产品协议里。

## 必须吸收的市场经验

- Basic Memory：Skill、MCP/CLI、渐进式工具发现、会话 hook；
- Supermemory / OpenMemory：跨 AI 客户端共享一个记忆入口；
- Letta：极小常驻状态和按需档案分层；
- Graphiti：事实有效时间、来源 Episode、矛盾失效而非覆盖；
- MemMachine：保留 ground truth 能显著降低有损抽取风险；
- Memvid：本地单文件、append、时间旅行和便携；
- YantrikDB：consolidation、decay、contradiction 是长期记忆卫生；
- seekdb/MatrixOrigin Memoria：数据库级 fork、merge、rollback；
- Qdrant：结果预算、结构化过滤与多路召回。

## 不复制的默认模式

- 不把完整聊天、PDF 或机械 chunk 当长期真相；
- 不把 Embedding API 变成第一版运行前提；
- 不只提供 `add/search/delete` 记忆箱接口；
- 不让动态索引常驻主 Agent system prompt；
- 不让 AI 直接修改 Page、索引或 Redo Log；
- 不把 Wiki/Markdown 变成双向同步真相源；
- 不把自动遗忘和合并做成无法回滚的后台黑箱。

## 最难形成壁垒的部分

- Go 单文件本身；
- BM25 或 B+ Tree；
- MCP/Skill 适配；
- Obsidian 导出；
- 给产品贴上 AI-native 标签。

这些都能被快速复制。

## 可能形成壁垒的部分

1. 自主 Schema 在长期运行中仍保持低熵；
2. 无原文、无向量 API 时仍有高质量写入和召回；
3. Context Pack 在严格 token 预算下仍保留足够证据；
4. AI 对已有知识做精确、可审计、可回滚的 merge/split/revise；
5. Codex、Claude 等陌生 Agent 真正可以零旧上下文接管；
6. 同一数据稳定导出为人可读 Wiki，而不牺牲数据库身份。
7. 单个逻辑库能作为安全、无宿主依赖的数据库包安装和直接问答，同时仍可加入全局跨库路由。

## 立即风险

- `Memora` 已有同领域开源项目使用，公开名称需要更换或做充分商标/包名检查；
- “不保存原文”与市场上 ground-truth-preserving 路线正面冲突；
- 主流高召回方案依赖 Embedding，纯 BM25/Router 的效果尚无证据；
- 自研完整存储内核可能消耗大量时间，却没有先证明 Agent 体验。

## 战略顺序

先证明“AI 自主维护 + 低上下文接管 + 精确修改”成立，再证明自研 Page/MVCC 内核。否则产品可能只得到一个数据库内核，而没有得到真正有差异的 Agent 数据体验。
