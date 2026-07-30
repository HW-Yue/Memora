# Conversation Delta 交接 v1

状态：F33 已实现并冻结。

## 显式事件

宿主通过 `memora reflect --event <JSON>` 发送
`memora.conversation-event/v1`。不依赖隐藏生命周期 hook。触发点是稳定结论
形成、用户明确要求记住、宿主 compaction 前 checkpoint，或宿主能观测到的
session end；不在每条消息后机械调用。

事件包含稳定 `event_id`、session、workspace、授权 Database 和三种 kind：

- `delta`：寒暄/草稿/重复项标为 `ignore`；一个事件最多携带一个需要落库的
  `persist` 项及完整 Mutation Plan；
- `checkpoint`：只保存 active Database、Route path 和 last event ID；
- `session_end`：显式清除该 session 的 checkpoint。

原始对话不进入 Event Journal。语义内容只作为 Mutation Plan 的参数经统一
MSQL 写入目标 Row。

## 幂等与权限

Journal 在调用写入 Tool 前持久化 event 指纹。相同 ID 和相同内容重试返回已
保存 Receipt，不再执行 MSQL；相同 ID 配不同内容返回 `revision_conflict`。
中断后仍为 processing 的事件标记为 in-doubt，禁止盲重试。确定失败会释放
reservation，允许宿主修复后重试。

Mutation Plan 的 `source_event_id` 必须等于 Event ID，目标 Database 必须与
delta 一致，且 Plan 授权只能是 Event 授权的子集。缺少 Database 或 Plan 时
返回 `needs_context`，并保证没有 Tool 调用。

Journal 只持久化指纹、处理状态、紧凑 Receipt 和 checkpoint。一次 Event 最多
提交一个原子语义 delta，避免多个独立事务产生不可判定的部分完成状态。多项目
切换使用新的显式 checkpoint 覆盖同一 session 的旧 checkpoint。

## Receipt

`memora.conversation-receipt/v1` 返回 processed、needs_context、checkpointed
或 ended，包含 ignored 数、Mutation Receipts、缺失项及 replay 标记。CLI 在
needs_context 时以失败退出码结束，防止宿主误报成功。

## 关联

- [Skill 写入流程 v1](./skill-write-v1.md)
- [上下文生命周期](../query/context-lifecycle.md)
- [Canonical Skill v1](./canonical-skill-v1.md)
