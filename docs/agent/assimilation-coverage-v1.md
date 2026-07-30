# 资料清单与覆盖 v1

状态：F35 已实现并冻结。

## 任务事件

宿主通过 `memora assimilate --event <JSON>` 发送
`memora.assimilation-event/v1`，按以下顺序管理一个临时任务：

```text
inventory → window* → checkpoint* → finish → clear
```

`inventory` 保存来源 ID、标题、短 locator、SHA-256 和结构单元。单元
类型为 source、directory、chapter、page、table 或 attachment，使用
parent ID 表达层级；可读单元声明半开 extent `[0,n)`，结构容器的
extent 为 0。默认所有可读单元必须覆盖，只有显式 optional 才不阻塞完成。

`window` 只记录 unit ID、已读半开范围和窗口 SHA-256，不记录原文。
重复窗口不增加 revision；重叠范围合并后再计算未读区间。同一范围
出现不同指纹视为来源变化，拒绝继续。

## 持久化与恢复

每个事件包含稳定 event ID、task/workspace 和 expected task revision。任务
inventory、已合并 coverage、窗口指纹和 checkpoint 与紧凑事件收据在同一
Store transaction 提交。同 ID 同内容重试返回 replay receipt；同 ID 异内容或
过期 revision 返回 `revision_conflict`。

checkpoint 仅保存 active unit、offset、有界 host cursor 和 last window event ID。
daemon 重启或宿主上下文中断后，任意新客户端可从收据中恢复 checkpoint
和未读范围，不需要旧对话。

## 完成与清理

`memora.assimilation-receipt/v1` 返回 in_progress、incomplete、
coverage_complete 或 cleared，包含 task revision、覆盖计数、未读范围、
checkpoint、replay 和 duplicate-window 标记。`finish` 遇到任一必读范围
未覆盖时返回 incomplete，CLI 失败退出，宿主不得宣称资料吸收成功。

coverage_complete 只表示可进入 F36 独立复核和语义提交，不表示知识已
写入。完成提交或取消后必须显式 `clear`；它只删除 Memora 临时任务，
同事务清除该任务的旧事件元数据，只保留不含 checkpoint 和未读范围的
cleared tombstone；绝不删除用户源文件。

## 禁止内容

事件模型没有 content、body、text 或 chunk 字段；严格 JSON 解码拒绝未知
字段。标题、单元 label、anchor 和 locator 均有字符上限，不能借结构元数据
持久化原文或机械 chunk。

## 关联

- [资料吸收](../data/assimilation.md)
- [Canonical Skill v1](./canonical-skill-v1.md)
- [Conversation Delta 交接 v1](./conversation-delta-v1.md)
