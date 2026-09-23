# Route 的描述句：必填的是内容，不是非空字符串

状态：**已实现**（分支 `feature/route-purpose-contract`，已 `--ff-only` 合回主线；下面"明确不在
这一块"的两项仍未做）。前置实测见 `docs/decisions.md`
「语义树的标签质量是可测量的检索损伤」及其补记。

## 问题

规范三处已写明每段 Route 的 `name`/`kind`/`purpose` 必填，且喂给 jev 的候选只留 name 与 purpose
（`docs/query/implicit-route-path-v1.md`、`docs/query/jev-tree-v1.md`、
`docs/query/retrieval-routes-jev.md`）。代码也确实这么喂：`choose_layer` 取 `(name, purpose)`，
`decide` 把 `{name: purpose or name}` 交给 `build_set_payload`。

实测 56 条 Route：`purpose == name`（名字复读）**44 条**，其中 `memora` 库 44 条无一是真描述；
`aliases` **0 条**；`synopsis` **0 条**。**"必填"被形式满足了**——非空字符串，内容等于名字。
而 `purpose or name` 这个兜底把"没写"和"写了名字"在管道里变成同一个东西，于是标签缺失永远不会被发现。

## 修法（一个 Feature，一个分支）

1. **去掉静默兜底**：purpose 缺失或等于 name 时不再拿名字顶上；该层要在走树的输出里如实暴露
   "这一层没有可用描述"（落在 evidence 或结果字段，不新增配置项）。
2. **写路径**：新建 Route 时 `purpose == name` 拒绝；修改存量 Route 只告警不拒绝（不把自己锁在库外）。
   判定前先规范化（trim、大小写、全半角）。
3. **体检**：doctor / admin 报出 `purpose == name` 的 Route（数量 + 路径），存量 44 条可见。

## 判据

- RED 先行：走树在"purpose 等于 name"的层上，输出里能看到该层没有可用描述；去掉兜底前后行为可区分。
- 写路径：新建 `purpose == name` 的 Route 被拒且回引本规则；改存量只告警。
- `doctor` 能在真库上报出存量（数字随回填下降）。
- 不引入新配置项；`SHOW ROUTES` 的列不变。

## 明确不在这一块

- **不回填那 44 条**：回填是按 `skills/memora/` 流程走 MSQL 的写库动作，不是代码改动，单列一步
  （顺序：`memora` 库优先；purpose 一句话说"装了什么"，aliases 补口语别名）。
- **synopsis 不接进走树**：那是第三块（0–1000 字长描述，`DESCRIBE ROUTE` 按需读）。

## 坑

- 去掉兜底后 `SHOW ROUTES` 会冒出一片"没有可用描述"，看着像回归——先跟使用者说清这是预期。
- 别顺手加个空串占位，那是把兜底换个马甲。
- 规范化比较，否则加个空格就绕过。
