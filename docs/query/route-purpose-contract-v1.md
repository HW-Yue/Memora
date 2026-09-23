# Route 的 purpose：必填的是描述，不是非空字符串

状态：**已实现**（2026-09-23）。实测依据见 `docs/decisions.md`
「语义树的标签质量是可测量的检索损伤」及其补记；实现计划见
[route-purpose-contract](../planning/route-purpose-contract.md)。

## 规则

每条 Route 的 `purpose` 必填，**要求的是一句"这里装着什么"**。`purpose` 为空、
或者规范化后等于 `name`，都算**没写**——两者是同一种缺失。

规范化（`router.FoldLabel`）：去首尾空白、内部空白折成一个空格、全角 ASCII 与
表意空格折成半角、大小写折平。比较不做 NFC/NFKC：当前依赖里没有 `x/text`，
而这里要挡的就是"同一个词换个写法"——宽度、大小写、空格（与
[隐式建路径](./implicit-route-path-v1.md) 里 name 匹配的取舍一致）。

## 五处行为

| 位置 | 行为 |
|---|---|
| **新建 Route** | **拒**（`validation_error`），错误文本回引本规则 |
| **改存量 Route** | **只告警**：`route_purpose_repeats_name` notice，带 `route_id`/`path`/`name` |
| **修存量 Route** | `ALTER ROUTE :route SET PURPOSE :purpose` —— **与新建同一条判定**：复读 name 则拒，写真描述则落盘并 revision++ |
| **走树** | 该候选以**空 purpose** 交给 jev；该层在 `evidence[].undescribed` 与结果的 `undescribed_at` 里如实列出 |
| **体检** | `memora doctor` 报 `routes_without_purpose`（数量）与 `routes_without_purpose_paths`（≤50 条 `库.表/路径`） |

"新建"覆盖三条写路径，共用 `router.CheckPurpose` 一处判定：
`CREATE ROUTE`（root 与 child）、`INSERT` 隐式路径**新建的每一段**、
ROUTE MUTATION 的每个 target（`Build` 与 `Validate` 两道）。

**改存量为什么只告警**：实测 44 条存量 Route 的 purpose 就是名字。拒绝存量等于
把写入者锁在最需要修的那个库外面。notice 出现在 `ALTER ROUTE` 的结果里
（RENAME 可能**新造成**这个状态，SET SYNOPSIS / SET ALIASES 则是照出它本来就在）。

**为什么必须能修**：只拒新的、修不了旧的，是引擎级自相矛盾——44 条存量会永久无法
修复。purpose 是**描述**不是身份，身份是 `route_id` 与位置；"冻结"只对
`scope`/`anti_scope` 成立（那是决定什么行能进的契约）。把描述冻在创建时刻，等于
要求写入者在信息最少的那一刻写出最终答案，而那正是 44 条复读的成因。

`SET PURPOSE` 上**不再出** `route_purpose_repeats_name` notice：这条语句要么给出
真描述、要么被拒，没有第三种结果。它要求 `expected_revision`（描述被盲写覆盖
等于两个写入者互相抹掉），并按 structural 授权。

**隐式路径命中已存在的段不判定**：那段不是新建的。`skillwrite` 的计划期校验同样
不判定——一段是新建还是复用，只有引擎在事务里知道。

## 不在这里

- **不回填存量**：回填是按 `skills/memora/` 流程走 MSQL 的写库动作，不是代码改动。
  `SET PURPOSE` 只是把它变成可能，`doctor` 的数字仍然是它的进度条。
- **不动库/表级语义**：`row_semantics` 与 Database/Table 的 `purpose`/`scope`/`anti_scope`
  建表后仍无语句可改，照这个形状补是另一块。
- **不重建派生层**：Route 的 `purpose`/`aliases` 不进召回索引（召回单元取自行的
  `title`/`summary`），走树每次现读节点，所以改 purpose 没有需要重建的下游。
- **不接 synopsis 进走树**：`DESCRIBE ROUTE` 的长描述是另一块。
- **不新增配置项**，`SHOW ROUTES` 的列不变。
- **aliases 为空不判定**：决策日志把它和 purpose 列在一起，但空 aliases 不会
  让候选看起来"有描述"，不属于同一个故障域。

## 关联

- [jev 走树](./jev-tree-v1.md) — 候选只带 name 与 purpose，所以描述缺失直接是检索损伤
- [INSERT 隐式建路径](./implicit-route-path-v1.md) — 每段 name/kind/purpose 必填
- [Route 重塑计划](./route-mutation-plan-v1.md) · [执行](./route-mutation-execution-v1.md)
- [结果信封](./result-envelope.md) — notice 的位置
