# history 只记原地修改，身份变化靠谱系指针

状态：**已实现**（2026-09-21）。指针落在新行 history 的首条记录上，`SHOW HISTORY` 以
`origins` 列读出；源行只改 `row_state` 与 `successor_ids`。

## 一句话

history 只记**一行自己的原地修改**。拆分、合并产生的新行各建自己的 history，
并由新 history **指向来源 history**；源行的 history 不因身份变化而增长，
**源行的 revision 也不推进**。

## 规则

| 场景 | history 怎么写 | revision |
|---|---|---|
| 原地改值 | 追加一条记录 | 该行 +1 |
| SPLIT（一 → 多） | 源行 history **不动**；每个新行各建自己的 history，都指向源行 history | 源行**不动**；新行从 1 起 |
| MERGE（多 → 一） | 新行 history 指向**多个**来源 history——所以指针是列表，不是单值 | 同上 |

**指针落在新行 history 的首条记录**（它创建的那一条）上；MERGE 是列表。

**源行 revision 不推进。** 身份变化不是这一行的内容版本：源行停在它最后一次原地修改的
revision 上，history 保持连续，`(row_id, revision)` 不出现没有记录的空洞。
`superseded` 这件事由**状态 + `successor_ids`** 表达，不由 revision 表达。

实现口径（2026-09-21）：源行那次写入只改 `row_state` 与 `successor_ids`——
`revision`、`commit_sequence`、`updated_at` 都停在最后一次原地修改上，也不追加 history
记录；这次身份变化照样进 `mem_changes`（审计面记的是事务，不是行的版本）。

源行仍留在主表（`superseded`），用一个字段（`successor_ids`）记录它拆分／合并后的新 row id，
读到时懒解析，见[数据行的生命周期](./row-lifecycle-successor.md)。

## 与 successor_ids：同一条身份边的两个方向，两个都留

| | 在哪 | 指向 | 服务谁 |
|---|---|---|---|
| `successor_ids` | **源行**上 | 前向：旧 → 新 | 读路径：拿旧 `row_id` 要立刻解析出当前行 |
| history 指针 | **history 表**里 | 后向：新 → 旧 | 回溯：从新行顺回身份变化之前的 history |

两者不是同一份事实存两遍，而是**一条边的两面**：都只存稳定 ID，都在同一事务落盘，
两个方向都是 O(1)。**两边都不能省**——只留 history 指针，「旧 `row_id` → 当前行」就得扫
history；只留 `successor_ids`，「新行 → 来源 history」就得扫全表找哪条 history 指向它。
两个都是读路径。

这是「同一份事实存两遍」的一个**写明了的例外**（[架构原则](./architecture-principles.md) §2），
与「叶子 `row_id` ↔ 行 `route_leaf_id`」同一个模式。源行的 `superseded` 状态与
`successor_ids` **同时存在**，不合并、不二选一。

删除不走这里：删除是[归档后物理删除](./row-delete-archive.md)，history 随行一起删。

## 关联

- [写入形态](./write-model.md) §1.2 — history 表形态
- [数据行的生命周期](./row-lifecycle-successor.md) — 废弃与接替、懒更新
- [行删除：归档后物理删除](./row-delete-archive.md)
