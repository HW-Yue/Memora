# history 只记原地修改，身份变化靠谱系指针

状态：**讨论稿**（2026-09-21 定方向；指针字段的位置与 `successor_ids` 的关系待定）。

## 一句话

history 只记**一行自己的原地修改**。拆分、合并产生的新行各建自己的 history，
并由新 history **指向来源 history**；源行的 history 不因身份变化而增长。

## 规则

| 场景 | history 怎么写 |
|---|---|
| 原地改值 | 该行 revision +1，追加一条记录 |
| SPLIT（一 → 多） | 源行 history **不动**；每个新行各建自己的 history，都指向源行 history |
| MERGE（多 → 一） | 新行 history 指向**多个**来源 history——所以指针是列表，不是单值 |

源行仍留在主表（`superseded`），另用**一个字段**记录它拆分／合并后的新 row id，
读到时懒解析——这条不变，见[数据行的生命周期](./row-lifecycle-successor.md)。

删除不走这里：删除是[归档后物理删除](./row-delete-archive.md)，history 随行一起删。

## 为什么

- 源行的 history 干净：它只说自己被改过什么，不被「它被拆了」这件事污染；
- 从任一新行都能顺指针回到身份变化之前的历史，回溯不断链；
- 身份变化改由指针表达，而不是往流水里插一条特殊记录。

## 未决

- **指针存哪**：新行 history 的首条记录上（本文默认），还是挂在行上；
- **与 `successor_ids` 的关系**：源行上那个「记录新 id」的字段和 history 指针都在表达
  「谁替代了谁」。[架构原则](./architecture-principles.md) §2 反对同一份事实存两遍，
  这一条下一轮连同 `successor_ids` 一起定。

## 关联

- [写入形态](./write-model.md) §1.2 — history 表形态
- [数据行的生命周期](./row-lifecycle-successor.md)
- [行删除：归档后物理删除](./row-delete-archive.md)
