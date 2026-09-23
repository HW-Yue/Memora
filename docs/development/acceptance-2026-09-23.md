# jev 走树验收（2026-09-23）：两臂实测 + 可靠性 + 一起事故

被验收的对象：`skills/memora/scripts/jev_tree.py`（jev 走树）、它的落点契约，以及刚补上的两处
（Route 的 `purpose` 可修订 + 44 条回填、叶子列表带出行号）。方法、数字、剩余缺陷与一起我自己的事故。

## 方法

- 两个**不带上下文**的 fresh agent（互不继承、不知道对方存在），**同一个要求**（跨库三问）：
  「向量 rekey 的机制是什么；我有哪几段实习；memora 项目当前的缺口有哪些」。
- **先后跑**（不并发，避免争抢 provider 与 daemon）；各自用 `time.time()` 在第一条查询前与结束前夹墙钟；
  各自把步骤日志写到 `/tmp/ab2/acceptance/arm{A,B}.md`；全程只读。
- **一臂禁止用 jev**（自己逐层走树，只有树走不通才允许召回兜底并记日志）；另一臂**只准用 Skill 的 jev 走树**。
  两臂都禁止读源码仓库、禁止上网。
- 判分用**事先从库里固定的 ground truth**（行号 + revision）：rekey = `memora.modules`
  `row_9da93d39061f46e7301696314bc47a84` rev1 ＋ `memora.decisions`
  `row_f122560ea1dc432437855a0c3eff1a80` rev1；实习 = `me.experiences` 两行；缺口 =
  `memora.modules` `row_7c3181885fa25e5c264a109637285129` rev2（②③⑤⑥ 未关、①④ 已关）。

## 两臂结果（各一次采样）

| | 自己逐层走树 | jev 走树 |
|---|---|---|
| **墙钟** | **67.6 s** | **58.0 s** |
| 工具调用 | 14 | 16 |
| MSQL 语句 | 31 | **6** 直接 + 35 在走树里 |
| 三问结果 | 3/3 全对，出处齐 | 3/3 全对，出处齐 |
| 代价 | — | 缺口那问要在 **34 个落点**里自己挑出对的那条；rekey 那问剔掉 1 条无关落点 |

**采样方差必须先说清**：同样这两个臂在前几轮是 44.9 s / 39.3 s（自己走）对 54.7 s / 65.1 s（jev）。
agent 自身的轮次耗时波动远大于两条路的差异，**单次采样不足以判定"谁更快"**。这一次 jev 首次在墙钟上
领先（58.0 对 67.6），但可靠的说法是下面这些**重复测出来的**数字。

## 脚本层（可重复，且是这次真正改变的东西）

| 问题 | 命中（4 次） | 墙钟 | 语句 | 落点 | reads |
|---|---|---|---|---|---|
| 当前的缺口有哪些 | **4/4** | 4.4–6.0 s | **15** | 34 | 2 |
| 我有哪几段实习 | **8/8** | 1.2–1.6 s | **5** | 2 | 1 |
| 向量 rekey 的机制 | **4/8** | 2.8–3.5 s | **9** | 5 | 1 |

**这些数字相对验收前的改动**：

- **语句 50 → 15**：`OPEN ROUTE` 从每叶一条（最宽那趟 35 条）归零——叶子列表现在直接带 `row_id` /
  `row_revision`（写路径"一个活跃行只挂一个叶子"的不变量直接变成读取端的列）。
- **上限删掉，只留时间阀（120 s）**：宽度/次数/深度三个读取端上限全删；停住的分支进 `stopped`
  （带原因与候选数）**不再混进 `landings`**；落点只有叶子，每条带 `(database, table, path,
  leaf_route_id, row_id, revision)`。
- **`reads` 读计划**：落点按 `(database, table)` 聚合并带上该表的列名（每表一次 `DESCRIBE TABLE`，
  不是假设——行的形状由引擎拥有）。最宽那问 34 个落点 → **2 条语句**就能读回来。
- **标签**：56 条 Route 里 44 条的 `purpose` 是名字复读 → **0**；`aliases` 0 → **44** 条写入；
  `doctor` 的 `routes_without_purpose` **44 → 0**。`purpose` 建完之后**原本没有任何语句能改**，
  所以先补了 `ALTER ROUTE … SET PURPOSE`（与 `CREATE ROUTE` 同一判定）。

## 剩余缺陷（都是重复测出来的，不是猜的）

1. **jev 在宽层可能给出错误的 `separated`——一次错选就静默丢掉正确的分支。**
   「向量 rekey 的机制」这一问，`memora:root`（5 个孩子）有时不答 `undecided` 而直接选中 `架构`，
   而 rekey 在 `接口与检索`：4 次跑 4/8 命中。**被选错时 walk 无法自知**（答案仍带 `incomplete: true`，
   因为更下层有 `undecided`）。
2. **jev 在高层的错误 `empty` 会把整棵树剪光。** 不喂 `aliases` 时，「当前的缺口有哪些」4 次里有
   **2 次返回 0 个落点**（0.9 s 就结束），而它本该命中。`pruned_at` 记下来是哪一层，但结果看着"走完了"。
3. **别名是权衡不是纯赚。** 同一组实验：喂 `aliases` 时缺口 4/4、rekey 4/8；不喂时缺口 2/4、rekey 8/8。
   合计 **12/12 对 10/12**，所以保留喂；但它换来的是"更果决"，果决在 rekey 那层恰好是错的。
4. **34 个落点的噪声**：`reads` 解决了 SQL 条数，但 agent 仍要在结果里自己挑（armB 明确记了这一步）。

## 一起事故：我重建的二进制没编 FTS5

- **现象**：我两次用**裸 `go build -o ~/.local/bin/memora ./cmd/memora`** 重建，漏了
  `-tags sqlite_fts5`（`README` 与 `docs/development/testing.md` 都写着"任何 `go build` 都必须带它"）。
  结果是真机 daemon 的**关键词召回直接失败**：`RECALL … MATCH` 返回
  `internal_error: query failed`——`scripts/ci.sh` 的注释正好警告这一点："a build without it produces
  a binary where recall silently has no index"。
- **我为什么没发现**：重建后我只核了 `version`（提交号对得上），**没有验能力**（没有跑一条关键词召回）。
- **修复**：按 README 的配方重建
  （`CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" go build -tags sqlite_fts5 -trimpath`），
  重启 daemon，复验关键词召回 `succeeded`、`doctor` healthy / integrity ok / `broken_recall_units=0`。
- **数据损伤：没有。** 回填走的是 `ALTER ROUTE`（不碰召回单元），期间没有任何行写入。
- **对验收的影响**：arm A 是在这个窗口里跑的，但它的步骤里**没有用过关键词召回**（SHOW/ATLAS/ROUTES/
  SELECT/census/doctor），所以 67.6 s 与 3/3 仍然成立，这一条如实记在这里。
- **建议（未做）**：**启动时断言可选 SQLite 模块存在**（缺 `fts5` 就拒绝服务，或至少 `doctor` 报出来）。
  现在这个失败模式太安静：二进制能起、大部分语句能用，只有召回在查询时才炸。

## 结论与下一步

**可以下结论的**：这条路的**成本与形状**已经对了——一趟宽意图 15 条语句、只到叶子、带读计划、
上限只剩时间阀；窄意图 1.2–1.6 s 且完整；标签与别名让"宽意图整棵树走完"成为常态。
**不能下结论的**：它是否比 agent 自己走更快（采样方差太大，只有单次样本）。

**下一步优先级**（按实测的伤害排）：

1. **让错误的 `separated` 可恢复**——这是现在最大的正确性风险。可做的：Skill 明确教"`incomplete: true`
   或落点很薄时，换个说法再问一次，或显式给 `table=`"；脚本侧可试"把层路径写进问题"。
2. **`empty` 的静默剪枝**——高层 `empty` 直接导致 0 落点，考虑在裁剪面过宽时把它降级为 `undecided`。
3. FTS5 守卫（上面那条）。
4. 落点噪声：`reads` 已减 SQL 成本，是否再给一个"最可能的少数几条"视图仍是开放问题（**不能再引入分数**）。
