# Speculative Discovery Skill v1

状态：F124e 已冻结并实现。

## 目标

Canonical Skill 在一次模型续推之前生成一组彼此独立、可并行的只读 `memora query`
调用：Configuration/Database、每个授权 Database 的紧凑 Table、Lexical 候选、可选
Vector 候选，以及至多两个由同主题 Route Frame 提示的 Table 根 Route。数据库调用数
可以增加，但中间不需要模型逐条决定，目标是减少 LLM 调用次数。

## Profile 与预算

版本为 `memora.speculative-discovery/v1`。默认硬上限：

- 最多 4 个 Database 进入投机 profile，超过后回到普通逐步发现；
- 全部 predictor 合计 8 个候选和 4096 UTF-8 bytes；
- Lexical + Vector 同时启用时按 4/4 candidates、2048/2048 bytes 确定性分配；
- 最多预取 2 个 Table root，每个最多 12 个 Route；
- 一轮最多 10 个 tool calls，Frame 总上下文上限 12000 UTF-8 bytes。

每项 Database-specific 调用都携带相同的精确授权集合。query/vector 只通过参数传递。
Vector 只有宿主已提供同 space 的归一化向量时才加入；无 encoder、unavailable 或 stale
不失败，Lexical 和 Router 仍继续。

## Route Frame

宿主记录 topic ID、精确调用清单、tool call 数、输出 UTF-8 bytes、各 predictor 的
snapshot/catalog revision/status、每个 root page snapshot、候选/预取用量和 truncation。
不同 predictor snapshot 不合并成一个伪 snapshot，但 catalog revision 必须一致。

候选和预取始终是 `navigation_only`，`AnswerReady` 恒为 false。模型必须从当前 Catalog
显式选择一个或多个 Table；候选未命中的 Table 仍可选择。被选 Table 有同 topic 的
当前 root 预取时可以复用，否则立即生成正常 `SHOW ROUTES ... AT ROOT` fallback。

topic 改变、Catalog revision 改变、root page/Route revision stale、上下文超限或任务结束
时丢弃旧 Frame。错误预取只能浪费预算，不能排除 Table、改变 Route 权威或进入回答。
最终答案仍要求 `OPEN ROUTE` locator 后按 RowID/revision `SELECT` 回表。

## 关联

- [Canonical Skill v1](./canonical-skill-v1.md)
- [Discovery Frame v1](../query/discovery-frame-v1.md)
- [语义路由投机预取](../query/speculative-route-prefetch.md)
- [F124e 开工与完成门](../planning/f124e-speculative-discovery-skill-gate.md)
