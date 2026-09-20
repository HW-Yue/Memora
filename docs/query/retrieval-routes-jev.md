# 检索路线与 jev 逐层选择

状态：**讨论稿**（2026-09-20）。基于 `rewrite/adr0011` 分支的 SQLite + sqlite-vec 内核，
不是 `main` 的自研引擎。结论尚未提升为 Feature 规格；第 4 节的三项前置缺陷未修之前
不能开工。

## 一句话

检索不只有一条路。内核提供两个正交的有界面——**逐层导航**与**融合发现**——
由 Skill 决定走哪条或全走；jev 只是导航面上 Agent 侧的选择器，不进内核。

## 1. 三条路线（Skill 编排，不是内核概念）

| 路线 | Agent 侧做什么 | 用的内核面 |
| --- | --- | --- |
| A 融合发现 | 一次拿到 top-N 语义路径，从那里落地或续走 | 融合发现面（**新增**） |
| B 逐层自读 | 自己读每层 child 的 name/purpose 选一个 | 逐层导航面（已有） |
| C 逐层 jev | 把每层 child 当 `Choice` 的 option 交给 jev 选 | 逐层导航面（已有，零改动） |

内核**不表达**「有几条路线」「选哪条」「全走」。三条路线返回同构结果（语义路径），
因此 Skill 可以任意组合；裁决也在 Skill 侧。

B 与 C 用同一个内核面，差别只在 Agent 侧的选择器是 LLM 还是 jev。

## 2. 返回值形态：路径 + 节点类型

融合发现面返回的每条候选至少是：

- **完整语义树路径**（表内相对，取自 `router.Node.Path`，不重算）；
- `database_id` / `table_id`（路径是表内相对的）；
- **节点类型**：`leaf` 或 `branch`；
- `object_id`：仅当命中对象是 Row 时给出（即 RowID）。

分数、理由、命中字段、预测器回执一律不返回，沿用
[候选预测器只给路径](./predictor-path-only-v1.md)。

### 类型决定后续动作

- **`leaf` + `object_id`**（Row 命中）：可直接 `SELECT ... WHERE row_id = :id` 回表；
- **`leaf` 无 `object_id`**（Route 叶子命中）：先 `OPEN ROUTE` 取 locator，再回表。
  这一跳不能省，实现时容易漏；
- **`branch`**：不能回表，只能从该节点继续逐层导航（路线 B 或 C）。

## 3. 终止与裁决（Skill 侧）

模型自己判断当前叶子是否满足需求；不满足则退回 `branch` 候选往下走。
方向与 [F221 Evidence 充分性](../planning/f221-evidence-sufficiency.md)一致：
零行或不足的读取**不终止导航**，无证据时拒绝作答。

成本账：判断「够不够」必须真读到内容，所以每个 `leaf` 候选都带一次回表。
top-5 全试即 5 次 `SELECT`。融合面的 `LIMIT` 应按这个账取值，不是越大越好。

## 4. 三项前置缺陷（未修不能开工）

### 4.1 route 与 row 向量混在一张 vec0 表里

`mem_vectors` 同时存 route 与 row 向量，而 kind 过滤发生在 KNN **之后**
（`internal/sqlstore/search.go:311-314`，`k = limit*4+16`）。库里 Row 远多于 Route 节点时，
全局最近的那批几乎全是 Row，`USING VECTOR` 的 route 候选会**接近零且不报错**，
数据越多越严重。

修法在 sqlite-vec 这一层：按 kind 分表，或用 vec0 的 metadata / partition key 做**预**过滤。

### 4.2 没有按路径寻址的导航入口

现役导航语句只接受 ID：`SHOW ROUTES UNDER :parent_id`、`OPEN ROUTE :leaf_id`。
Parser 里没有任何按 path 寻址的入口。于是「只返回路径」的候选**无法被用于导航**，
模型只能看。

候选修法：新增 `SHOW ROUTES UNDER PATH :path` 与 `OPEN ROUTE AT PATH :path`
（倾向此条：路径对人和模型都可读），或把 `route_id` 加回候选。

### 4.3 Row 向量需要一次产品裁定

`indexRow`（`internal/sqlstore/search.go:90-91`）把表名 + row semantics + 全部列值
送去做 embedding，即
[ADR-0007](../decisions/0007-route-predictor-arsenal.md) 与
[存储索引边界](../storage/indexing.md) 禁止的「Row 正文向量副本」。

产品意图是：Row 向量命中后**返回该 Row 的语义路径**，事实仍由 SQL 回表。
但 F169 冻结了「一个 Leaf 最多一个活跃 Row」，`open_locators` 基数为 `0..1`，
因此一条 Row 的叶子路径**唯一指向那一行**——「只返回路径」在 Row 命中这条线上
不构成任何实际约束，等价于向量直达事实，只多一跳。

这是红线位置的改变，必须由独立 ADR 裁定，不能作为「反正只返回路径」的实现细节。
两个候选形态：

- **直给叶子路径**：一跳到底，但语义树在检索中退化为可解释性装饰，路线 B/C 失去意义；
- **上卷到祖先 branch**：向量只回答「在哪个语义区域」，最后一跳仍由 Agent 或 jev 判断，
  语义树保持主路径；代价是多一到两次导航往返。

## 5. 两条通道的可见性等级不同

词法 postings 在写事务内同步维护（`mem_postings`）；向量 embedding 在**提交之后**
异步计算，因为要调远程模型（`internal/sqlstore/search.go:24-25`）。

RRF 把两个新鲜度不同的排名混在一起，对用户表现为「刚写的东西搜不到，过一会儿又能搜到」。
融合面**必须显式声明可见性语义**，三个候选：等待（生产不可接受，含远程调用）、
在信封里标明向量通道可能滞后、或对新对象只走词法。开工前定，不留给实现默认。

## 6. 融合本身

RRF 只用 rank、没有权重可调，因此不触
[confirmed-directions 第 51 条](../archive/planning/confirmed-directions.md)
「禁止设置或调优融合权重」。但 F21 MATCH Fusion 属**已撤销**方向，
恢复一个融合面需要 ADR 显式标注差异：**只输出语义路径，不输出事实候选**。

实现形态倾向做成 `SHOW ROUTE CANDIDATES FROM ALL TABLES USING FUSED :query LIMIT :n BYTES :b`
——现有语句的第三个 predictor，而不是新语句，这样自动继承「只是提示、不是答案来源、
失败即 `not_found`、不承担版本契约」的既有边界。

在 SQLite 上实现是纯查询：vec0 那侧已 `ORDER BY distance`，postings 那侧按命中词数排，
两边各取 `row_number()` 作 rank，`1/(k+rank)` 相加。不需要新表或新索引设施。

## 7. 逐层调用的基建（已验证可用）

`memora query '<MSQL>' --input '<JSON>'` 将结果以 JSON envelope 写入 stdout，
退出码反映 `ok`，`readquery.Validate` 限制只能执行 SHOW / DESCRIBE / SELECT /
OPEN ROUTE / 只读 PLAN。脚本可循环：解析 JSON → 取 child → 填入下一次 `--input` → 再调。

**逐层 jev 不需要任何新内核能力。** 已知代价：每次调用是一次新进程 + 新 daemon 连接，
逐层 5 次即 5 次进程启动，叠在 jev 的 5 次 RTT 上。若实测过慢，出路是 `internal/ipc`
长连接或单进程内多语句，不是改内核。

## 8. jev 的定位

jev 是 TypeSafe 的托管 System One 模型，走 HTTP API，返回带校准概率的类型化判断
（`Choice` / `Noul` / `Score`）。候选集**放进请求**发送并计入 token，因此
「一层有多少 child」仍然直接换算成请求体、成本与延迟——**它不取消 fan-out 上限，
只是把上限的来源从「LLM 单层提示准确率」换成「一次 `Choice` 能可靠区分多少 option」**。

写入路径本轮**不动**，判断者仍是原有 Agent，因此
[F223 的结构 fan-out 硬上限](../planning/f223-route-branch-fanout-limit.md)原样保留。

jev 在 Skill 侧（host 侧）调用，不进引擎，符合 ADR-0007「引擎不内置供应商调用」。

## 待决

- 4.3 的红线位置（直给叶子路径 vs 上卷到 branch）；
- 第 5 节的可见性语义三选一；
- 4.2 选按路径寻址还是恢复 `route_id`；
- 融合面 `LIMIT` 的默认值（受第 3 节回表成本约束）；
- jev 的 `Choice` 在多少 option 下仍可靠，以及是否引入 no-match 出口。

## 关联

- [候选预测器只给路径](./predictor-path-only-v1.md)
- [语义 Router](./semantic-routing.md)
- [ADR-0007：Router 权威，候选预测器可组合](../decisions/0007-route-predictor-arsenal.md)
- [ADR-0011：存储引擎只做数据库](../decisions/0011-pure-storage-engine-tables-everything.md)
- [F223：Route Branch Fan-out 硬上限](../planning/f223-route-branch-fanout-limit.md)
- [F221：Evidence 充分性与导航终止](../planning/f221-evidence-sufficiency.md)
