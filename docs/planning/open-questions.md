# 尚未确认的问题

更新时间：2026-07-29。按“会不会让产品方向失败”排序，不按实现章节排序。

## 决策分工

本清单是风险跟踪表，不是逐项向产品负责人提问的问卷。

- 只有会改变用户可见语义、AI 自主权、数据保留承诺、跨设备冲突结果或不可逆兼容边界的问题，才请求产品负责人确认。
- 可逆的工程选择由实现侧直接决定，包括文件编码、ID 形式、Page/Extent 初值、IPC 细节、缓存参数、校验方式和内部索引结构；选择应优先保证正确性、简洁性和演化空间，并通过 ADR 记录。
- 质量、延迟、召回率、阈值和资源预算不靠偏好拍板，通过 benchmark 与故障注入确定。
- 尚未进入当前阶段的产品能力只记录，不提前阻塞 v0；到达对应里程碑时再确认。

语义冲突边界已经确认：数据库只处理并发与约束冲突；Skill 向用户展示互相矛盾的内容，并按用户指示重写。按主题合并后，尚需以后确认的产品边界只剩三类：多设备冲突语义、Wiki 是否允许回流和 AI 配置优化权限；另有发布前的公开身份外部核验。它们当前都不阻塞 v0，其余工程问题由实现侧自行收敛。

## Gate 0：发布身份

方向已确认：

- `Memora` 作为公开品牌，不再仅作为内部代号；
- CLI、Go module、GitHub 仓库、包名和域名等技术标识优先使用 `memora`，不可用时统一使用 `memoradb`。

发布前执行项：核验各平台可用性、同领域区分和商标风险。它不再是产品设计分歧。

## Gate 1：AI-native 是否成立

3. 不保存原始对话和大资料后，怎样独立证明吸收没有遗漏或曲解？
4. Mutation Agent 判断“值得长期保存”的 precision/recall 能否达到可用水平？
5. 每个 Database 的 Agent `0.8`、机械 N-gram `0.2` 初始权重，以及 Router、alias 和关系信号，怎样归一化和校准才能在无 Embedding 时达到召回门槛？
6. AI 连续自主建模后，怎样发现同义 Database/Table/Column 和结构熵增？
7. 文本 Column 启动默认 1200 字符的可演化上限怎样适配代码、表格和复杂论证，并让 Agent 正确选择切分或调整 Schema？
8. Codex/Claude Skill 的 Context Pack 怎样既短又保留足够证据和不确定性？
9. Skill 查询的延迟、token 和费用能否优于 Markdown/搜索基线？

## Gate 2：Agent 与协议

10. 第一版只提供 CLI+Skill，还是同时提供很薄的 MCP adapter？
11. MSQL v0 的正式 EBNF 和首批语句边界是什么？
12. SHOW/DESCRIBE/ROUTE、MATCH、历史和关系遍历在统一 envelope 中的字段是什么？
13. Skill 的 write 流程在稳定结论、会话结束、checkpoint 前还是显式指令时触发？
14. 外部调用方怎样稳定传入 conversation delta、event ID、workspace 和权限 scope？
15. Skill 的语义冲突展示 envelope 和用户指令怎样稳定转换为下一次 SQL 修改计划？
16. 多 Database 增长时根目录怎样保持短小且不漏掉冷库？
16.1 Router 内部 fan-out、叶子 ID 容量、最大深度、beam width、子树/整库重建阈值、generation 验收和语义 split/merge 协议是什么？

## Gate 3：数据治理

17. 事实有效时间和数据库提交时间是否都作为一等时间维度？
18. 关系由统一系统表管理，还是允许每个 Database 定义专门关系 Table？
19. AI 自动 merge/rename/migration 的等价判断、影响计划和回滚协议是什么？
20. 当前 revision、现实有效时间和用户定义的业务状态怎样共同决定默认查询范围？
21. Source Receipt 保存哪些锚点才有用，又不会重新变成文档仓库？
22. decay/consolidation 只生成候选，还是允许在某些 Policy 下自动提交？

## Gate 4：运行时与缓存

23. daemon 与交互式 `memora`、`--stdio` bridge、单次 CLI 的本地 IPC、会话、退出和崩溃恢复协议是什么？
23.1 macOS 用户级 daemon 的 launchd 注册、Unix socket、PID 和锁文件位置是什么？
24. InnoDB 风格 Buffer Pool 的 Instance 总预算、young/old 参数、分片门槛、刷脏阈值和预热比例；
25. Query Workspace 如何持久化，以及模型会话何时延续、checkpoint 和重建？
26. 是否需要独立的 Plan Cache 或 Query Result Cache；若需要，怎样绑定依赖版本和权限 scope？
27. 多个 daemon 客户端怎样隔离事务、Query Workspace 和权限 scope，并共享 Buffer Pool、commit sequence 与 compaction？

## Gate 5：存储内核

28. 是否以及何时增加 `READ UNCOMMITTED` / `SERIALIZABLE`，gap/next-key lock 与死锁检测怎样实现？
29. 最新 Record 直接放入 Clustered Index 叶子，还是由窄 Row Directory 指向独立 Record Store？
30. in-place + Redo Log、Copy-on-Write 或混合持久化；Undo Log 的物理形式；
31. Secondary Index 和 Posting Run 怎样与 Row 事务原子可见？
32. 16 KiB Page、1 MiB Extent、256 MiB Data File 和 8 KiB 页内阈值是否合适？
33. 内部 Row ID 使用递增整数、UUIDv7 还是双层标识？
34. overflow、checksum、压缩、加密和 format migration 的具体格式；
35. 端到端体验达到什么门槛后，才值得替换原型存储为完整自研内核？
36. Row-based Binlog 的 before/after image 编码、Group Commit 故障处理，以及 device/event ID、保留位点、幂等和环路抑制协议是什么？

## Gate 6：Wiki 与可携带性

37. 文件名采用纯 ID 还是 `slug--short-id`，移动后是否生成 redirect stub？
38. Export Profile 怎样由自主 Schema 自动生成和校验？
39. 是否永远保持单向导出，还是以后支持显式 diff/plan 导入？
40. 跨 Database 关系和部分 Route 导出怎样生成稳定 Wikilink？

## Gate 7：可演化配置（最后讨论）

41. 哪些配置在建库时确定后冻结，哪些只能迁移，哪些允许用户运行时修改？
42. 哪些配置允许 AI 优化，所需证据、benchmark、观察期、审批和回滚条件是什么？
43. 数据库包携带的配置怎样与安装方本地 Policy 合并，哪些值必须拒绝或覆盖？

## 下一步

先解决 Gate 0～2 并执行 [质量 benchmark](../product/quality-model.md)，再冻结 Page 格式。底层参数不是当前最大风险。
