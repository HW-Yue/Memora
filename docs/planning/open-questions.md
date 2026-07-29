# 尚未确认的问题

更新时间：2026-07-29。按“会不会让产品方向失败”排序，不按实现章节排序。

## Gate 0：发布身份

方向已确认：

- `Memora` 作为公开品牌，不再仅作为内部代号；
- CLI、Go module、GitHub 仓库、包名和域名等技术标识优先使用 `memora`，不可用时统一使用 `memoradb`。

发布前执行项：核验各平台可用性、同领域区分和商标风险。它不再是产品设计分歧。

## Gate 1：AI-native 是否成立

3. 不保存原始对话和大资料后，怎样独立证明吸收没有遗漏或曲解？
4. Mutation Agent 判断“值得长期保存”的 precision/recall 能否达到可用水平？
5. Router、BM25、中文 N-gram、alias 和关系图能否在无 Embedding 时达到召回门槛？
6. AI 连续自主建模后，怎样发现同义 Database/Table/Column 和结构熵增？
7. 约 800 字预算怎样适配代码、表格、复杂论证，并可靠触发 split/merge？
8. 内置 Agent Runtime 的 Context Pack 怎样既短又保留足够证据和不确定性？
9. 内置 loop 的延迟、token 和费用能否优于让外部 Agent 自行发现和查询？

## Gate 2：Agent 与协议

10. 第一版只提供 CLI+Skill，还是同时提供很薄的 MCP adapter？
11. MSQL 是 MySQL SQL 子集加扩展，还是追求更广兼容；正式 EBNF 是什么？
12. SHOW/DESCRIBE/ROUTE、SEARCH/MATCH、历史和关系遍历的稳定 JSON 格式是什么？
13. 内置 loop 的 write profile 在稳定结论、会话结束、checkpoint 前还是显式指令时触发？
14. 外部调用方怎样稳定传入 conversation delta、event ID、workspace 和权限 scope？
15. 写入是否先产生 candidate/disputed 状态，何时自动晋升为 active？
16. 多 Database 增长时根目录怎样保持短小且不漏掉冷库？

## Gate 3：数据治理

17. 事实有效时间和数据库提交时间是否都作为一等时间维度？
18. 关系由统一系统表管理，还是允许每个 Database 定义专门关系 Table？
19. AI 自动 merge/rename/migration 的等价判断、影响计划和回滚协议是什么？
20. 低置信度、矛盾、过期和被 supersede 的 Row 怎样参与默认查询？
21. Source Receipt 保存哪些锚点才有用，又不会重新变成文档仓库？
22. decay/consolidation 只生成候选，还是允许在某些 Policy 下自动提交？

## Gate 4：运行时与缓存

23. 交互式 `memora` 与 `memora --stdio` 的会话、退出、崩溃恢复和并发打开协议是什么？
24. Session/Warm LRU 的 Instance 总预算、Database 子预算、TTL 和 Bootstrap 大小；
25. Query Workspace 如何持久化，以及模型会话何时延续、checkpoint 和重建？
26. 是否缓存 Context Pack，还是只缓存带 revision 的 Row 定位？
27. 不使用 daemon 时，多 CLI 进程怎样协调锁、commit sequence、各自的 Buffer Pool 和 compaction？

## Gate 5：存储内核

28. Snapshot Isolation 是否足够，哪些 MSQL 操作需要 Serializable？
29. Row Directory + Version Store 是否优于真正的 Clustered Index？
30. in-place + Redo Log、Copy-on-Write 或混合持久化；Undo Log 的物理形式；
31. Secondary Index 和 Posting Run 怎样与 Row 事务原子可见？
32. 16 KiB Page、1 MiB Extent、256 MiB Data File 和 8 KiB 页内阈值是否合适？
33. 内部 Row ID 使用递增整数、UUIDv7 还是双层标识？
34. overflow、checksum、压缩、加密和 format migration 的具体格式；
35. 端到端体验达到什么门槛后，才值得替换原型存储为完整自研内核？

## Gate 6：Wiki 与可携带性

36. 文件名采用纯 ID 还是 `slug--short-id`，移动后是否生成 redirect stub？
37. Export Profile 怎样由自主 Schema 自动生成和校验？
38. 是否永远保持单向导出，还是以后支持显式 diff/plan 导入？
39. 跨 Database 关系和部分 Route 导出怎样生成稳定 Wikilink？

## 下一步

先解决 Gate 0～2 并执行 [质量 benchmark](../product/quality-model.md)，再冻结 Page 格式。底层参数不是当前最大风险。
