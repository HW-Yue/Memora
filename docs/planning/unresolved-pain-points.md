# 仍未解决的痛点

更新时间：2026-07-29。

| 痛点 | 当前缓解方向 | 尚未解决的核心 |
| --- | --- | --- |
| `Memora` 同领域重名 | 公开品牌固定；技术标识采用 `memora` 优先、`memoradb` 兜底 | 发布前的可用性、商标和用户混淆核验 |
| Markdown 越写越长 | 主题小文件、数据库语义记录 | AI 是否能长期维持正确拆分 |
| 查询索引污染上下文 | 内置 Agent Runtime 管理 Query Workspace 和模型输入 | 最终 Context Pack 仍需严格限流 |
| 每次查询重复发现 | 长驻交互 CLI、`--stdio`、分层 LRU 与 Warm LRU | checkpoint/warm-cache、预算和调用方 scope |
| 自动写入污染长期记忆 | 内置 Runtime 的 write profile、先查后改 | “值得写”判断和触发时机 |
| Router 选错分支会漏检 | 倒排索引兜底 | 三路结果的稳定排序方法 |
| Schema 出现同义表/字段 | AI 自动 merge、别名和事务迁移 | 等价判断错误时如何回滚 |
| 保存所有对话会膨胀 | 只保存状态变化 | 怎样可靠判断“值得保存” |
| 不保存原文节省空间 | Source Receipt、吸收 inventory、独立复核 | 与 ground-truth-preserving 路线相比能否保证质量 |
| 记录约 800 字便于读取 | 单主题语义模块 | 自动 split/merge 的判断标准 |
| 修改已有知识困难 | SQL、revision、MVCC | 语义冲突能否安全自动合并 |
| 多 Agent 同时修改 | expected revision、短事务 | Schema 并发和字段级冲突 |
| 无向量的语义检索 | BM25、N-gram、别名、关系、Query Agent 扩词 | 与 Vector baseline 的实际差距 |
| SQL 对 Agent 标准化 | MSQL、Parser、结构化错误 | 方言规模和实现成本 |
| Wiki 便于人类阅读 | 单向确定性导出 | 人类编辑如何安全回流 |
| 从零做存储引擎成本高 | 先做端到端语义原型 | 何时值得实现完整 Page/Undo Log/Redo Log |

## 当前最危险的假设

1. 认为 Skill 可以控制宿主上下文；实际上它只能指导，不能删除历史。
2. 认为 AI 吸收资料后一定正确；丢弃原文使错误可能永久化。
3. 认为 AI 会自然维护好 Schema；长期使用可能产生结构熵增。
4. 认为短 Router 一定比一次全文查询省 token；多轮导航可能更贵。
5. 认为 800 字适合所有知识类型；代码、表格和复杂论证可能不同。
6. 认为 Go 单文件和自研存储内核本身能形成产品壁垒；真正壁垒必须来自长期自治质量。

## 近期验证重点

- 上下文 token、工具输出和主题切换实验；
- AI 连续 50 次自主建模后的 Schema 健康度；
- 不同表达方式下的无向量召回率；
- revision 冲突和错误修改回退；
- 同一数据库由 Codex 与 Claude 交替接管；
- 与 Basic Memory、Mem0/Vector baseline 做同任务对照；
- 发布公共标识前完成同领域项目、包管理器、域名和商标冲突检查。
