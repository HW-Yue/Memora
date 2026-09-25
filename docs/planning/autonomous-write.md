# 写入落点自主

状态：**已实现，待 Review 后合入主线**（分支 `skill/autonomous-landing`；用户 2026-09-25 拍板并授权开工）。
定论记在 [决策日志 2026-09-25](../decisions.md)，修订 [2026-09-22 第 8 条](../decisions.md) 里
"库层要人确认"与"0 个/≥2 个停下问人"两句。

## 问题

用户诉求：写入全自动——agent 自己定库、定表、定叶子并写入，**不停下问人**。

挡住它的不是引擎（已核实）：

- L1「局部、可逆、同库写入」本来就自动执行（[AI 自主权](../archive/agent/autonomy.md) 风险等级）；
- `memora schema --plan` 自己发 `CREATE DATABASE` / `CREATE TABLE`，授权用 `default_level=L2`、
  **不带 approval**（`internal/skillschema/runner.go` 的 `request()`）；
- 只有 `APPLY SCHEMA CHANGE`、`APPLY ROUTE MUTATION`、安装 package 要 hash 绑定的人工 approval。

停下问人的只有两处策略：Skill 的 ask-first 规则（`references/discover-and-read.md:37`）
和 2026-09-22 那条"库层要人确认"。而 `archive/agent/autonomy.md` 的四层治理第 4 条本来写的是
"Agent 自主区：**自主建库、建表和维护数据**"——ask-first 是后来 Skill 侧的收紧，两者一直矛盾。

## 已定（2026-09-25）

一条判据，库/表/叶子共用，**没有任何分支回抛给用户**：

- 候选恰为 1、且未命中其 `anti_scope` → 绑定，继续选表、选叶子、写入；
- 候选 ≥2 → **agent 自己裁决**（jev floor probe 或自身判断），选一个、写一处；
- 候选为 0 → **agent 自己新建一个库并写入**（purpose/scope 一并写出来），不问；只有它判断"这条根本不值得记"时才不写，并说明为什么；
- 命中 `anti_scope`（**只有写了才有这一条**）→ agent 自己否决该库，按剩余候选重算；

替代人工确认的是**回执**：每次写入在回答里报 `库.表` + Route 路径 + `row_id` +
命中的 `purpose`/`scope` 原句；有过歧义时同时列出被放弃的候选与放弃理由。

## 已改的位置（分支 `skill/autonomous-landing`）

**Skill —— 四处，六副本已同步（2 adapter + 3 安装位 + 发布仓库）**

- `references/discover-and-read.md`：删"先问用户用哪个"，与无人在环分支并成一条判据；
  "Bind authorization only after the user has named a Database" 改为"绑到你选定的那个库"。
- `references/write.md`：落点判断明确归 agent（不问用户）；规则 2 补"0 候选自己建库，
  但那是**看完所有既有 purpose 之后**的最后一招"；新增 **Report the landing** 段。
- `scripts/jev_select.py`：docstring 的 `confidence` 那句改为"look further, or decide yourself"；
  `TYPESAFE_API_KEY` 缺失时的报错去掉"or ask the user"。
- `scripts/jev_tree.py`：同上那句报错。
- 收尾已跑：`--repo` → 三个 `--install` → `--publish`（发布仓库还要自己提交推送）。

**仓库文档（旧结论与新结论冲突，按 AGENTS.md 标记取代）**

- `docs/decisions.md` 2026-09-22 第 8 项：已加"已被取代"标记。
- `docs/query/jev-tree-v1.md` 写入侧那段：已加 2026-09-25 修订标记。
- `docs/agent/skill-write-v1.md:43-44`（"语义冲突、高风险或越权仍由 Skill 请求用户"）**不动**——
  它管的是语义冲突与越权，不是落点。

**代码**

- **零改动**（已核实：L1 自动、`schema --plan` 的 L2 无 approval、
  授权 scope 允许声明一个尚不存在的库名）。

**验收**：`go test ./internal/skilldoc/ ./internal/devgate/` 全绿；
`sync-skill.sh --check` 报 every copy matches。

## 开做前的判断（2026-09-25，codex `gpt-6-astra`）

**最大风险：把"落点由 agent 裁决"写成"证据不足也必须猜一个"。** 候选数量不是语义适配度——
恰好一个也可能不匹配，多个可能职责重叠，零个可能只是发现不完整；贸然建库会让一份记忆碎成几个库。
落点回执只能让误写**可追溯**，不能证明写对；新建库的 `purpose`/`scope` 是 agent 自己写的，
更不能当独立匹配证据。lint 能保证示例形状与语法，兜不住语义误路由。

**落进文本的三处**：① 先看再绑（读 `purpose`/`scope`/`anti_scope`，并说明排除了哪些）；
② 低置信度是"继续查"的信号，不是可以绑定的许可；③ 新建库是"看完所有既有 purpose 之后"的
最后一招，不是第一反应。方向不变：**取消用户确认，不取消 agent 的查证责任。**

## 已知风险与缺口

1. **回执成了唯一控制面**。既然事前不再确认，回执就必须是写入的必交项且要具体到能被复核；
   否则写错库既没人拦、事后也没人看得见。
2. **伪唯一**：库的 `scope` 写宽了会命中一切，等于把越界自动化。对策：匹配保守、
   回执必须引用命中的原句。
3. **`anti_scope` 只是 agent 的放置提示，永远不进引擎**（2026-09-25 定，见
   [决策日志](../decisions.md)）。命中与否是主观业务判断，引擎"不替 AI 判断业务含义"；
   而且它是**看时机才写**的字段——只有存在会捕获同一批内容的邻库时才值得写。
   引擎能拒绝的只有它真能判的东西（`read_only` 的已安装库已经在拒）。
4. **纠正成本**：写错叶子靠 MOVE；把行换库大概率是 L2。自主写入放开后，纠正路径的摩擦要一起看。

## 一块就够

Skill 的落点自主（四处文本）加上面两处文档取代标记。纯文本改动，**没有代码块**，一个分支承载。
做完：六副本同步 + 发布仓库推送，`skilldoc` / `devgate` 全绿。

## 已搁置（2026-09-25，用户定：先不做，不再提）

MCP 的发现入口：`memora_execute` 每条语句强制非空 `authorized_databases`，所以冷启动发不出
全量 `SHOW DATABASES`，全自动在 MCP 上迈不出第一步。**不排期、不作待定跟踪、以后不再提起**，
此处仅存档。

## 待定

- 落点回执的字段与去重（避免每行都重复一遍长 purpose 原句）。
