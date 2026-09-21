# 决策日志

状态：运行中的判断记录，不是 ADR 系列；规范级结论仍进 [`decisions/`](./decisions/)。
按 `~/.dsh/AGENTS.md` 约定，向顾问咨询后的结论当场写在这里。追加，不覆盖。

## 2026-09-20 · SQLite 基座如何成为开发基线

对象：`rewrite/adr0011`（领先 `origin/main` 32 个提交，`main` 独有提交 0）如何落到默认分支。
状态：**候选，待授权**。

**结论**：在 `main` 上落**一个**「SQLite baseline」提交（`git merge --squash` 或等价做法），
`rewrite/adr0011` 保留为远端存档 tag（如 `archive/adr0011-rewrite`）；不做 fast-forward。

**理由**

- 32 个提交是一次改写的收敛轨迹（含「先删错再修回」），不是 32 个独立 Feature。
  留在 `main` 上会长期污染 `blame` 与 `bisect` 的信噪比。
- 基线的语义是**一个点**，不是一段过程。ADR-0011 已承载这次改写的解释价值，
  提交序列是冗余的第二份叙事。
- 仓库规则要求每项 Feature 独立 Review、授权、验收、合入。这 32 个提交从未走过该流程，
  也从未跑过 CI；fast-forward 等于追认一批未合规提交为「已合规历史」。

**弃选**：fast-forward 全部 32 个提交。代价是放弃改写过程的逐提交可 bisect 性，
以及单个删除动作的 `blame` 归因粒度——改写以删除为主，损失小。

**前置条件（与选项无关，必须先做）**：`.github/workflows/ci.yml` 给整个 gate run 设了
`CGO_ENABLED=1`。Go 只在 `CGO_ENABLED` **未设**时才在交叉编译时自动关 cgo，
因此 `GOOS=linux` sweep 在 macOS runner 上必然失败，GitHub CI 必红。
本地实测：不设该变量 `./scripts/ci.sh` 全绿（EXIT=0）；设成 `1` 则死在 `vet`（EXIT=1）。

## 2026-09-20 · 落地顺序：先修 CI，再压基线，再补核心包回归

对象：`rewrite/adr0011` 落地这一块的拆分、顺序与最大风险。
状态：**方向性结论，待授权**（顾问咨询后落盘；不改变上一条「压一个 baseline 提交」的候选地位）。

**结论**：顺序是 修 `ci.yml` → 在分支上见一次真实绿 CI → squash 一个 baseline 提交落 `main`、
分支留 tag → 补 `catalog`／`row`／`instance` 三个核心包的最小回归 → 再开「行必须可导航」。

**理由**

- `ci.sh` 全绿 ≠ GitHub CI 会绿：本地 macOS 与 runner 的 cgo／交叉编译环境不是一回事；
- 先见绿 CI 再 squash：否则 `main` 首次 CI 就红，回滚一个 22.5k 行的 squash 提交代价极大；
- squash 前至少让 `catalog`／`row` 有可跑回归，否则 34 个提交压成一个不可二分点，
  将来回归只能靠读代码定位（15 个包现在零测试，集中在 cli、daemon、adminapi、instance、
  catalog、skillschema、skillwrite、routetrace、row）。

**弃选**：先做「行必须可导航」再落地。产品上它确实是下一件，但把它压在一个从未见过
CI 的基线之上，等于把两处风险叠在同一提交里。

**前置（与选项无关）**：`.github/workflows/ci.yml` 把 `CGO_ENABLED=1` 设在整个 gate run 上，
`GOOS=linux` sweep 在 macOS runner 上必然失败。应把它下沉到需要 cgo 的 job／stage。

## 2026-09-20 · 停用 F 流水号

**结论**：现役工作用题目，不再编号。`F1`–`F228` 只作为旧引擎考古标签。
分支 `feature/<short-name>`。ADR 仍用四位编号（0002–0012）。

**理由**：流水号来自自研引擎 TDD 序列。现行文档曾经引用九十多个 F 编号，
实体规划只剩四个，代码完全不认识它们。再从 F229 往下续，只会把考古和现役混在一起。

**弃选**：另起 `S1` / `N1` 新序列。新数字同样会在下一次改写后变成噪音。

## 2026-09-20 · 现役文档只留当前形态

**结论**：`docs/planning/` 只留队列、TDD、产品门和「行必须可导航」讨论稿。
F 时代 Feature 稿与过程稿进 archive。现役规格去掉「目标形态已改 / 不能当设计依据」横幅。
一叶一行和 fan-out 写在 [写入形态](./product/write-model.md)，不再单开 planning 文件。
