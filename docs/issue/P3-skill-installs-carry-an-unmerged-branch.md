# P3 · Skill 的仓库外四处安装位带着一个**未合分支**的文本

严重度：high（分叉已经在线上，且 `--check` 在分支上会说"every copy matches"）。状态：**待做**（2026-09-26 发现）。

## 症状

在**主线**上跑 `scripts/sync-skill.sh --check` 报 8 处不一致：

```
drift: ~/.agents/skills/memora  differs in references/discover-and-read.md
drift: ~/.agents/skills/memora  differs in references/write.md
drift: ~/.claude/skills/memora  differs in references/{discover-and-read,write}.md
drift: ~/.cursor/skills/memora  differs in references/{discover-and-read,write}.md
drift: ~/Developer/memora-skill/memora differs in references/{discover-and-read,write}.md
```

四个仓库外位置里的这两个文件，与**未合入的** `feat/alter-database-description` 逐字相同
（已 `diff` 核实），而主线上的版本是旧的。

## 为什么一直没被发现

`--check` 比较的是"当前工作区"，而工作区在 2026-09-25 停在 `feat/alter-database-description` 上 ——
在那个分支上，仓库副本 + 两个 adapter 副本 + 四个仓库外位置**恰好全都一致**，于是报
"every copy matches"。切回主线（2026-09-26）之后才暴露。

也就是说：**`--check` 的结论依赖你在哪条分支上**，而 Skill 的六个位置里，四个不在仓库里、
不受分支控制。

## 后果

- DSH 从 `~/.agents/skills/memora` 加载 Skill —— 现在 agent 读到的是未合分支的文本文。
- 发布仓库 `~/Developer/memora-skill` 同样带着它：任何从那里安装的人拿到的是主线从未承认的内容。

## 已定的处置方向（二选一，先定方向再动手）

1. **把 `feat/alter-database-description` 走完成门合入主线** —— 它的两个 Skill 文件改动正是这四个位置
   现在的内容，合完两边自然一致。这条分支本来就差 review + 合入（测试齐全、决策已写）。
2. **从主线重新同步这四个位置**（`--install` × 3 + `--publish` + 推送）—— 等于把未合分支的 Skill 文本回退。
   注意：这三个安装位与发布仓库都在工作区之外，本次会话的沙箱**不允许写**，要另开一个能写的环境。

在方向定下来之前，不要再对 Skill 做任何"顺手同步"。

## 顺带的结论

`--check` 只能证明"当前位置的副本一致"，不能证明"安装位与主线一致"。要么在 CI/收尾流程里加一条
**以主线为准**的比对，要么至少在切分支时知道这条命令的答案可能翻转。
