# F225：Row 必须可展示（写入时强制 summary）

状态：候选，2026-08-11 提出；尚未 Review、尚未获得实现授权。
与 [F224](./f224-mandatory-row-route.md) 同形状、同执行点，是写入侧的第二个不变量。

## 问题

`summary` 是 Row 的核心展示字段，但写入时完全不要求它存在或非空。

引擎侧（`internal/catalog/columns.go:146`）`summary` 确实是受控 semantic role，
与 `title`／`identity`／`status`／`fact`／`rationale` 并列。但**唯一的约束是唯一性**
（`uniqueDisplayRoles`，`:153`）：一个 Table 最多一个 summary 列。没有任何地方要求
Table 必须有 summary 列，也没有任何地方要求写入时它非空。

Skill 侧问题更直接：

- **「Write」章节（SKILL.md 263–292）一个字没提 summary。** 整节讲
  `route_leaf_ids`、expected revision、plan 形状——全是机制，没说该往 Row 里写什么内容；
- 唯一详细的规则在「Assimilate sources」（387–395）：「完整自足的 Markdown 文档、
  约 1,000 汉字、配 `TEXT(2500)`、绝不静默截断」。但那节只覆盖资料吸收，
  日常写入不走那条路；
- 「Write」章节的示例是 `"summary":"Route results are locators only"`——
  五个词的短句，**与 1,000 汉字的规则直接矛盾**，而 Agent 在写入时读到的正是它；
- 建表时没有任何引导声明 `ROLE 'summary'`；`semantic_role` 在整个 SKILL.md 里
  只出现一次（432 行的 schema change 示例），讲的还是 title。

结果：Row 写进去了，但没有可展示的正文。

## 唯一主要结果

任何提交后处于 live 状态的 Row，其 `summary` role 列必须存在且值非空。
不满足的写入一律失败，返回结构化信封与可执行出路。

## 不变量与边界

约束提交后状态，不是请求字段：

- **INSERT**：结果 Row 的 summary 非空（trim 后长度 > 0）；
- **UPDATE**：不触碰 summary 的更新保留既有值并成功；把 summary 置空的更新失败；
- **DELETE / tombstone**：豁免；
- **SPLIT / MERGE**：每个 live 目标各自满足。

**Table 没有 summary role 列时，写入失败**，错误指明用 schema change 加列。
这不是独立的建表约束——它是行级不变量的必然前提：没有列就无法满足。
存量 Table 在被写入之前不受影响。

## 引擎判定「非空」，不判定「好」

引擎只判定它真正能判定的机械属性：**trim 后非空**。

**引擎不判定 summary 的质量、长度或是否自足。** 「约 1,000 汉字的完整自足 Markdown
文档」是语义判断，属于 Agent，由 Skill 强约束承载。引擎假装能判定质量只会给出
虚假保证。这与宪章一致：AI 决定语义结构，引擎保证物理正确性。

## Skill 强约束（与引擎改动同批交付）

`skills/memora/SKILL.md` 必须改，否则 Skill 与引擎互相矛盾：

1. **「Write」章节**增加硬要求：每个 INSERT/UPDATE 必须写 summary；summary 是
   完整自足的 Markdown 文档（约 1,000 汉字），不是摘要、不是要点列表、不是一句话；
2. **修掉矛盾示例**：`"summary":"Route results are locators only"` 改为符合规则的样例，
   或改用省略号明确标注这是截断展示而非真实长度；
3. **「Decide where knowledge lives」章节**增加：新建 Table 必须声明一个
   `ROLE 'summary'` 列，且 TEXT 上限足以容纳约 1,000 汉字加 Markdown 语法
   （如 `TEXT(2500)`）；1,200 的默认上限不够；
4. **同步修掉 F224 的冲突**：Write 章节现在写着 route_leaf_ids
   "including an explicit empty array"——明文允许空数组，正是 F224 要禁止的。
   改为「必须至少一个 leaf；空数组不再合法」。

adapters 为生成物，改完 canonical 后重新生成。

## 存量数据

已存在的空 summary Row **不追溯清理**，不阻塞 reopen。新增 `semantichealth` finding
`KindUnsummarizedRow = "unsummarized_row"`，与既有 `unrouted_row` 并列，
由 Agent 决定补写还是删除。本 Feature 不实现自动补写。

## 明确不做

- 不由引擎判定 summary 的质量、长度下限或语言；
- 不追溯迁移存量空 summary Row；
- 不在本 Feature 强制 summary 列的最小 TEXT 宽度（列太窄会导致 Agent 无法写入
  合规正文，属真实问题，但需要独立冻结迁移路径，另立 Feature）；
- 不改 `summary` role 的既有唯一性语义。

## RED 与完成门

- RED 先证明：summary 为空或缺列的 INSERT 当前提交成功；
- INSERT 缺 summary 列、summary 为空串、summary 仅空白三种情形均失败，
  且不留下部分写入；
- UPDATE 不触碰 summary 时保留既有值并成功（防止回归）；
- UPDATE 把 summary 置空时失败；
- DELETE 与 tombstone 不受影响；
- Table 无 summary role 列时写入失败，错误含加列的可执行出路；
- `msql.execute` 直连与 skillwrite plan 两条路径**都**被拦
  （证明执行点在引擎，与 F224 同一位置）；
- 存量空 summary 数据库仍可 reopen、可读，并被 `unsummarized_row` 报告；
- SKILL.md 四项改动落地，adapters 重新生成，`skillcontract` 校验通过；
- 目标测试、`-race`、Agent import allowlist 与完整 CI 全绿。

## 关联

- [执行计划](./execution-plan.md)
- [F224 Row 必须可导航](./f224-mandatory-row-route.md) — 同执行点的姊妹不变量
- [F223 Route Branch Fan-out 硬上限](./f223-route-branch-fanout-limit.md) — 处理形状来源
- [语义记录模型](../data/semantic-records.md)
