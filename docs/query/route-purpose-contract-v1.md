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

## 三处行为

| 位置 | 行为 |
|---|---|
| **新建 Route** | **拒**（`validation_error`），错误文本回引本规则 |
| **改存量 Route** | **只告警**：`route_purpose_repeats_name` notice，带 `route_id`/`path`/`name` |
| **走树** | 该候选以**空 purpose** 交给 jev；该层在 `evidence[].undescribed` 与结果的 `undescribed_at` 里如实列出 |
| **体检** | `memora doctor` 报 `routes_without_purpose`（数量）与 `routes_without_purpose_paths`（≤50 条 `库.表/路径`） |

"新建"覆盖三条写路径，共用 `router.CheckPurpose` 一处判定：
`CREATE ROUTE`（root 与 child）、`INSERT` 隐式路径**新建的每一段**、
ROUTE MUTATION 的每个 target（`Build` 与 `Validate` 两道）。

**改存量为什么只告警**：实测 44 条存量 Route 的 purpose 就是名字。拒绝存量等于
把写入者锁在最需要修的那个库外面。notice 出现在 `ALTER ROUTE` 的结果里
（RENAME 可能**新造成**这个状态，SET SYNOPSIS / SET ALIASES 则是照出它本来就在）。

**隐式路径命中已存在的段不判定**：那段不是新建的。`skillwrite` 的计划期校验同样
不判定——一段是新建还是复用，只有引擎在事务里知道。

## 不在这里

- **不回填存量**：回填是按 `skills/memora/` 流程走 MSQL 的写库动作，不是代码改动。
  `doctor` 的数字就是它的进度条。
- **不接 synopsis 进走树**：`DESCRIBE ROUTE` 的长描述是另一块。
- **不新增配置项**，`SHOW ROUTES` 的列不变。
- **aliases 为空不判定**：决策日志把它和 purpose 列在一起，但空 aliases 不会
  让候选看起来"有描述"，不属于同一个故障域。

## 关联

- [jev 走树](./jev-tree-v1.md) — 候选只带 name 与 purpose，所以描述缺失直接是检索损伤
- [INSERT 隐式建路径](./implicit-route-path-v1.md) — 每段 name/kind/purpose 必填
- [Route 重塑计划](./route-mutation-plan-v1.md) · [执行](./route-mutation-execution-v1.md)
- [结果信封](./result-envelope.md) — notice 的位置
