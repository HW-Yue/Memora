# INSERT 隐式建路径 v1

状态：**已实现**（2026-09-21）。属于
[Agent 与引擎的分界](./agent-engine-boundary.md) 第 2 项的落地。

## 一句话

`INSERT` 可以直接给一条**路径**：Agent 逐段给出 name、kind、purpose，引擎在同一事务里
复用已有的段、补齐缺失的段，拿到末端叶子后照常挂载。名字与用途都由 Agent 给，
所以补齐是这次写入的**确定后果**，不是引擎猜语义。

## wire 形状

新增 option `route_path`，与 `route_leaf_ids` **互斥**（同时给即拒）：

```json
{"mutation": {"route_path": [
  {"name": "architecture", "kind": "branch", "purpose": "架构决策"},
  {"name": "sqlite", "kind": "leaf", "purpose": "为什么选 SQLite"}
]}}
```

- 每段 **`name`、`kind`、`purpose` 都必填**；kind 显式写，不由「是不是最后一段」推断；
- **不支持 alias 与 synopsis**：带了就拒（要这些走显式 `CREATE ROUTE`），不静默忽略；
- 路径从该表的 root 之下开始，**不含 root 段**。

## 逐段规则

1. **表没有 root → 拒**。root 的 purpose 是全表语义，Agent 没给就不替它编；
2. 匹配**只按 name、同父唯一、大小写不敏感**，不看 alias——alias 是查询侧的软入口，
   拿它建树会让同一条路径落两处。大小写不敏感是**有意的**：引擎自己判同父重名就是
   `EqualFold`，解析比创建更严会在"名字已存在但大小写不同"时走进死路；
3. name 先 `TrimSpace`、空即拒、含 `/` 即拒（与显式 `CREATE ROUTE` 同一条规则）；
4. **中途命中 leaf 还要继续下钻 → 拒**（叶子已挂行，改 branch 是结构迁移，不是 INSERT 的后果）；
5. **最后一段命中 branch → 拒**（它不定位任何行）；命中 leaf → **复用**，且
   **purpose 不一致即拒**（不静默改写、也不假装成功）；
6. 新建段按最终状态过**扇出上限**（默认 12），中间层同样检查；越界硬失败、整事务回滚，
   不留半条链；
7. 解析出的叶子若已被别的 live 行占用 → 按既有的挂载拒绝处理（一行只占一个叶子）。

## 不做

- **不改 SPLIT／MERGE**：它们的多目标仍走显式 `target_route_leaf_ids`；
- 不把「路径已存在」当幂等成功——命中已存在的 leaf 且 purpose 不同会拒，语义撞名不掩盖；
- 不含 NFC 归一化（当前依赖只有三个模块，`x/text` 要新增依赖）。name 的规则是
  `TrimSpace` + 精确匹配，所以前后空白不会分叉；**组合字符导致的视觉同名是已知缺口**，
  要不要补随契约版本一起定。

## 关联

- [Agent 与引擎的分界](./agent-engine-boundary.md) — 为什么补齐算「确定后果」
- [Route 配套表](../product/route-companion-table.md) — 树的结构与致空规则
- [MSQL Mutation](./msql-mutation.md) — option 所在位置
