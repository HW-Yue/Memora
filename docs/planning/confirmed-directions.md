# 已形成的方向

更新时间：2026-07-29。这里记录方向性结论，不代表实现已验证。

1. Memora 是 AI 自主建模和维护的个人数据库；
2. AI 决定业务数据库、表、字段和语义关系；
3. 引擎负责 Page、索引、事务、MVCC、Undo Log、Redo Log 和恢复；
4. 所有正式操作通过版本化标准语言 MSQL；
5. Agent 先发现数据库、Schema 和 Router，再用 SQL 取数；
6. Route 只导航，实际数据只能通过 SQL 查询；
7. 语义记录是短小完整的知识模块，目标约 800 字；
8. 不保存完整大文档、图片和机械 chunk；资料仅临时读取并吸收；
9. 第一版不依赖向量 API；
10. 检索结合语义 Router、倒排索引和关系图；
11. 修改带 revision，保留历史并处理并发冲突；
12. 动态索引不进入长期 system prompt；
13. 数据库查询优先交给 Memora 内置 Agent Runtime 的 read profile，外部调用方只接收受预算约束的 Context Pack 或最终回答；
14. 可导出为带 `[[Wikilink]]` 的 Obsidian Wiki；
15. Wiki 是单向派生快照，不是第一阶段的真相源；
16. 设计文档按主题拆分，归档不作为日常上下文；
17. 外部宿主不需要支持 sub-agent；可直接调用 `memora ask`，也可通过 `memora exec` 自行执行 MSQL；
18. 默认采用一个本地 Memora Instance、多逻辑 Database、每库多 Table 的层级；
19. 最近 Database、Route、Schema 和记录定位使用分层 LRU，支持跨聊天热启动；
20. 缓存是可丢弃的性能状态，必须通过版本校验失效，不能成为事实来源；
21. 存储术语与 MySQL/InnoDB 对齐；相同概念使用相同名称，AI-native 独有概念保留 Memora 名称；
22. 核心引擎和 CLI 使用 Go，目标是本地单可执行文件、低安装成本和方便 Agent 调用；
23. CLI + Skill 是第一阶段标准接入面，MSQL 是 CLI 内唯一正式数据操作语言；
24. Table 不与物理 Data File 强绑定，查询按 Page 定位读取，不把整个文件载入内存；
25. 一个可导出的语义 Row 对应一个 Markdown 页面，内部关系、索引和 Undo Log 不单独导出；
26. Data Dictionary 必须自描述用途、边界、Row 语义、别名和版本，使陌生 Agent 无需旧聊天即可接管；
27. 写入交给内置 Agent Runtime 的 write profile，由它先查重再选择 ignore/insert/revise/merge/split，并返回短收据；
28. `Memora` 确定作为公开品牌，不再仅作为内部代号；
29. 技术标识统一优先使用 `memora`，不可用时统一使用 `memoradb`，不在不同平台临时采用不同后缀。
30. Memora 内置可独立运行的 Agent Runtime，用户可以配置自己的模型提供方和 API Key；
31. 自然语言由内置 loop 转换为 MSQL，内置 loop 与外部 CLI/SDK 调用统一经过同一 Parser、Policy、事务和执行器；
32. 内置 Agent 不能绕过 MSQL 操作物理存储；查询、写入和 Schema 变更通过同一 Runtime 的不同能力配置隔离。
33. `memora` CLI 进程直接承载 Instance、执行引擎和 Agent Runtime，不使用后台 daemon 或 socket；默认命令进入前台交互循环，`--stdio` 为外部 Agent 提供长驻 JSONL 会话。
34. 一个逻辑 Database 是可独立分发的产品对象：可导出为自描述包，在另一台机器校验后一键安装，也可由 CLI 以默认只读方式直接打开问答；包格式和命令名仍待原型确定。
35. 默认 `memora`/`memora ask` 提供 Instance 级全局记忆入口，先跨 Database 路由再执行有界 MSQL；“全局”不取消 Database 的项目、隐私、导出和删除边界。
36. 数据库包只承载声明式数据和元数据，不携带可执行安装逻辑、模型密钥、宿主聊天或运行时缓存；未信任包不能借文本获得 prompt 或工具权限。

## 尚需验证

上述结论需要最小端到端原型验证：

```text
自然对话
→ AI 建库/建表
→ Router 导航
→ SQL 查询
→ revision 更新
→ 新 Agent 接管
→ Wiki 导出
```
