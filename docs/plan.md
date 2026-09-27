# 计划过程稿

本文件是**过程稿**：向顾问咨询后的结论与待定点，铺在这里等用户点头。
成熟以后，结论提升为规格／ADR，或并入[执行计划](./planning/execution-plan.md)；
**未解决的缺陷不进这里**，进[问题活账](./issue/README.md)。

---

## 2026-09-26 · 整理之后：现状怎么读、下一步什么顺序

对象：仓库整理完（`main` 归位、本地分支 408 → 9、`docs/` 三分）之后，现状与优先级。
顾问：Codex 子 agent（本次可用；同日早些时候 Codex 两次、Claude Code 两次均失败）。

### 现状（顾问原话，三句）

1. 产品主干已经成形：SQLite 是唯一存储基座，语义树与 MSQL 构成核心，四条检索路径都已实现。
2. 9-26 的主线与文档整理完成，但整理后首次主线 CI 就暴露出冷缓存下的阻断故障。
3. 主要风险不是缺功能，而是**门禁、分支与 Skill 安装副本未对齐**，加上若干数据正确性与运维缺口。

我的补充：这三句与事实一致，且第 3 句是这次整理最值钱的产出 —— 11 条未解决项里，
只有 B1／B2+B3／B5／B6／B7／A6 属于"功能缺口"，P2／P3／C1／C2 都是"已经写在某处、
但没人按它验收"的那一类。

### 建议顺序（顾问，4 项）

1. 先修并验证**冷缓存 CI 红门**（[P2](./issue/P2-real-ci-dies-on-a-cold-module-cache.md)），
   让主线重新有可靠门禁。
2. **Review 并合入 `feat/alter-database-description`**，随后同步四个仓库外 Skill 安装位，
   消除未合分支内容的漂移（[P3](./issue/P3-skill-installs-carry-an-unmerged-branch.md)）。
3. 修 `mutate` 的向量排干（[C2](./issue/C2-mutate-does-not-drain-vectors.md)）与 Skill 里两处
   写入/检查指引（[C1](./issue/C1-write-md-root-detection-is-wrong.md)），再补一条
   **安装位一致性验证**。
4. 更新过期的唯一队列，再从高风险数据正确性问题（[B1](./issue/B1-membership-moves-bypass-the-write-path.md)、
   [B2+B3](./issue/B2-B3-tree-cycle-guard-and-hidden-failures.md)、
   [B5](./issue/B5-repair-recall-units-never-converges.md)）里选一项，按 TDD 单项推进。
   **暂不启动新里程碑。**

顺序的理由（我补的）：第 1 步之前，任何"绿"都没有背书；第 2 步不解决，Skill 的六个位置
就继续各说各话；第 3 步是**推荐路径本身是错的**，代价最低、影响最广；第 4 步才回到功能。

### 弃选（顾问）

- 先开新里程碑、之后再补门禁与收口。
- 一次打包处理所有 issue。
- 现在去追五条八月旧分支和未授权的规划。

### 待定点（需要你定；没点头不动手）

- **第 2 步的同步方向**：本沙箱不能写仓库外那四个安装位。是合入 alter 分支让两边自然一致，
  还是从主线反向同步（等于把未合分支的那两处 Skill 文本回退掉）。另外 5 条八月旧线分支
  （只在本地、远端没有对应）是推到 `attic/` 留档后删，还是直接删。
- **第 4 步先做哪一项**（B1 / B2+B3 / B5），以及 `m7-recall-plan.md`、`jev-routing-ladder.md`
  是否维持"待授权 / 未开工"不变。
