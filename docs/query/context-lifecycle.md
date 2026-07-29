# 上下文生命周期

状态：首选方向改为内置 Agent Runtime；上下文重建和预算仍需原型验证。

## 问题

Agent 为了查询需要读取数据库、Schema、Router、候选列表和结果。这些内容如果一直留在对话上下文中，会造成：

- token 持续增长；
- 已切换话题的旧索引干扰推理；
- 旧 Schema 和 Route 误导新查询；
- 多个数据库的信息互相污染；
- 查询工具输出比最终答案更占上下文。

如果全部丢弃，同一话题的下一次查询又要重复发现和导航。

## 已确认原则

- 动态数据库索引不能写进长期 system prompt 或 Skill；
- Skill 只保存稳定语法、流程和错误恢复规则；
- 从第一次响应开始就限制输出，而不是事后依赖压缩；
- 同一主题只需要保留最小“当前工作目录”；
- 主题切换或版本失效后不能继续使用旧 Route；
- 导航缓存不能长期占用 MVCC snapshot。

## 当前首选：内置 Agent Runtime

外部调用方把查询意图、scope hint 和结果预算交给 Memora 内置 Agent Runtime。Runtime 在受控模型上下文里读取 Router、Schema、SQL 错误和候选，只向调用方返回最终回答或 Context Pack。

Runtime 由 Memora 管理 prompt、工具协议、Query Workspace、缓存失效和输出预算，不依赖宿主能否创建 sub-agent。内置 loop 生成的 MSQL 与外部直接提交的 MSQL 使用同一执行核心。详见 [内置 Agent Runtime](../agent/embedded-agent-runtime.md)。

## 降级方案：三层缓存

### L0：Route Frame

模型上下文只保留一个短状态块：

```text
db=project_memora
route=/indexing/routing
schema=12 route_rev=31
tables=design_topics,relations
focus=语义路由与SQL
```

目标约 50～150 tokens。

### L1：Navigation Session Cache

完整 Router、短句柄和候选路径由 CLI 或 Agent 会话保存，用 cursor 复用，不重复返回全文。缓存绑定 database ID、Schema version、Route revision 和 TTL。

缓存采用 Database 分区、Route/Table 为内部项的分层 LRU；另设可跨聊天复用的 Warm LRU。具体见 [工作集与 LRU 缓存](./working-set-cache.md)。

### L2：Engine Cache

B+ Tree Page、Data Dictionary、posting 和执行计划留在引擎内，不进入模型上下文。

## 查询生命周期候选

```text
调用方提交查询意图
→ Runtime 恢复紧凑 Query Workspace
→ 内置 loop 发现/路由/执行 MSQL
→ Runtime 返回受预算约束的回答或 Context Pack
→ Runtime 保存可重建 checkpoint，模型上下文可结束或轮换
→ 调用方使用结果继续工作
→ 需要长期保存的状态通过独立写入流程提交
```

## 关键现实限制

Memora 可以控制内置 loop 的模型输入和工具输出，但不能突破模型本身的上下文上限，也不能删除外部宿主已经接收的结果。因此 Runtime 保存的是可重建工作状态，而不是无限增长的原始模型对话。

Runtime 默认运行在前台 `memora` 交互进程中；外部 Agent 可以通过 `memora --stdio` 持有同一个长驻进程。单次命令退出后由持久化 checkpoint 和 warm-cache 重建，不依赖后台 daemon 或 socket。

因此第一版必须优先依赖：

- 极短的 SHOW/DESCRIBE/ROUTE 输出；
- 强制 LIMIT 和字段投影；
- cursor 避免重复输出；
- 查询结果分页；
- 不返回无关元数据；
- 宿主支持时才使用 compaction 或隐藏 tool state。

不能把“稍后从上下文删除”作为数据库正确性的前提。

## 缓存失效

Route Frame 至少绑定：

```text
database_id + schema_version + route_id + route_revision
```

失效返回 `NAVIGATION_CACHE_STALE`，Agent 重新 DESCRIBE/ROUTE。缓存只保存版本指纹，不保持长事务。

## 仍未确认

1. Codex、Claude Code 的 sub-agent 返回结果实际占用多少主上下文？
2. Query Agent 应保持一次性启动，还是在实测延迟过高后按数据库短期复用？
3. 如何强制 Context Pack 大小、字段和证据格式？
4. 不支持 sub-agent 的宿主怎样接收紧凑的 LRU Bootstrap？
5. 主题切换由主 Agent 判断还是数据库提供 topic hint？
6. 输出预算按字符还是针对具体模型估算 token？
7. 查询结果怎样压缩，同时保留证据和可追溯性？
8. 什么时候直接全文搜索比逐层 Router 更省上下文？
9. 上下文节省是否值得 sub-agent 启动延迟和额外 token？
10. write profile 的 checkpoint 怎样与 read profile 隔离，避免未提交计划污染后续查询？

## 必须做的实验

- 在 Codex 和 Claude Code 各运行一组 20～50 轮连续查询；
- 比较无 Router、全 Router、Route Frame + cursor 三种方案；
- 记录输入 token、工具输出、查询轮数、延迟和召回正确率；
- 测试主题频繁切换和 Schema 中途改变；
- 测试宿主 compaction 后 cursor 和 Route Frame 是否仍可恢复。

## 关联

- [未解决痛点](../planning/unresolved-pain-points.md)
- [语义路由](./semantic-routing.md)
