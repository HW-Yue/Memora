# Skill 语义冲突交互 v1

状态：F34 已实现并冻结。

## Conflict View

Skill 在待写提议与已回表 Row 互相矛盾时，构造
`memora.semantic-conflict/v1`，不执行 MSQL 修改。View 只包含：

- conflict ID、Database/Table 和当前授权集；
- 待写 proposal 的完整值与 actor/event/reason；
- 最多 10 个已回表 Row 的 Row ID、revision、完整值和来源；
- 按字段名稳定排序的 proposal/existing 差异，显式区分
  NULL 与字段缺失。

View 是临时宿主对象，不写入 Row、History、Event Journal 或隐藏
Store；不包含 SQL 或 Mutation Plan。数据库不增加
candidate/disputed 状态。

## 用户决议

宿主必须向用户并列展示来源、revision 和差异。用户明确决定后，
宿主构造 `memora.conflict-resolution/v1`，并使用新的
`source_event_id`：

- `RETAIN`：保留指定现有 Row，转换为 `IGNORE` Plan；
- `REWRITE`：按用户批准内容改写指定 Row，转换为 `REVISE` Plan；
- `REMOVE`：保留一个 survivor 并逻辑删除其他冲突 Row，转换为
  `MERGE` Plan。

Resolution 必须绑定原 conflict ID、授权子集和用户指令。Plan 的
Database/Table、actor、source event 和 reason 必须与 Resolution 一致；
UPDATE/DELETE 的 target 和 expected revision 必须来自 View。不允许借决议
扩大授权或修改未展示 Row。

Resolver 只返回通过 F31 Policy 的新 Mutation Plan，不执行 Tool。宿主再通过
`memora reflect`/`memora mutate` 提交，使并发 revision 冲突仍由引擎阻止。

## 关联

- [Skill 写入流程 v1](./skill-write-v1.md)
- [Conversation Delta 交接 v1](./conversation-delta-v1.md)
- [Canonical Skill v1](./canonical-skill-v1.md)
