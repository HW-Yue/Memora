# 资料独立复核与提交 v1

状态：F36 实现规格；F78 已增加来源强度和 challenge-bound review，旧的
“仅比较 context ID”门已被取代。

## 提交对象

宿主在 F35 返回 `coverage_complete` 后，通过
`memora assimilate --submission <JSON>` 提交
`memora.assimilation-submission/v1`。提交包含：

- task/workspace、覆盖 revision、作者与草稿 SHA-256；
- 完整语义模块及其 Mutation Plan；
- 结构化关系及其 RELATE Plan；
- 关键数字/事实的 value SHA-256 与短 source anchor；
- `memora.assimilation-review/v1` 独立复核证明；
- 尚未解决的语义冲突 ID。

提交可以携带 AI 整理后的完整语义模块，但不得携带原始窗口、机械 chunk、
逐字摘录或二进制资料。每个模块和关系必须至少有一个落在 inventory 可读单元
内的半开 source anchor；每个关键数字/事实必须单独绑定模块、字段、value
SHA-256 和 anchor。

关系端点使用本提交内已复核的 module ID，而不是预猜引擎 Row ID。模块计划
验证完成后，控制器用 Mutation Receipt 的实际 object ID 绑定 RELATE 的
`:source`/`:target`；被关系引用的模块因此必须产生恰好一个逻辑对象。

## 独立复核门禁

复核必须由不同于 draft author 的 reviewer 身份完成，且 reviewer context 与
draft context 不同。coverage 完成后，引擎发出一次 challenge；review artifact
必须把 challenge、相同草稿哈希、覆盖 revision、完整模块/关系/关键事实 ID
集合、findings digest，以及 anchor、关键数字、冲突和“未保存原文”四项检查
绑定进统一 SHA-256。只提交两个自报 context ID 不再通过。

引擎验证 challenge 和 artifact 的一致性；真正启动独立上下文由宿主负责。没有
可信宿主签名时，不把隐藏上下文隔离描述成密码学可证明事实。

任一必读范围未覆盖、复核拒绝/漏项、anchor 越界或计划 provenance 不一致时，
Memora 在任何 Tool 写调用前拒绝提交。存在未决冲突时返回 `needs_user`，宿主按
F34 展示并等待用户决议，不能自行选择版本。

## 写入与恢复

每个模块仍使用 `memora.mutation-plan/v1` 的 INSERT、REVISE、MERGE、SPLIT、
MOVE 或 IGNORE；关系只使用 RELATE。所有计划的 actor/source event、授权范围、
preflight、revision guard 和 verify 继续由既有 Policy 校验。

单个 Mutation Plan 是一个短事务。多个计划不伪装成资料级原子事务：提交 ID
在首个 Tool 调用前持久化为 processing；若调用中断或任一已提交变化未能验证，
结果为 `in_doubt`，相同提交不得盲目重放，宿主必须查询当前 Row/revision 后用
新提交恢复。完整成功后相同内容重试只重放 Source Receipt，不再调用 Tool；
相同 ID 不同内容返回 revision conflict。

## Source Receipt

`memora.source-receipt/v1` 只长期保存并返回：

- 来源 ID、标题、短 locator 和内容 SHA-256；
- 原始来源 kind、`reviewed_source` 强度和 review artifact digest；
- task/submission、覆盖 revision、作者和 reviewer；
- 模块/关系对应的 plan、decision、逻辑对象、revision/commit sequence；
- 关键事实的 value SHA-256 与短 source anchor；
- committed、needs_user 或 in_doubt 状态与有界 warning。

Receipt 不保存 Mutation Plan、字段正文、原始窗口或复核推理。可通过
`memora assimilate --receipt <submission-id>` 重取。只有 committed 才表示语义
提交成功；之后宿主显式发送 F35 `clear` 事件清除临时 task，Source Receipt
继续保留，且永不删除用户源文件。

## 关联

- [资料清单与覆盖 v1](./assimilation-coverage-v1.md)
- [Skill 写入流程 v1](./skill-write-v1.md)
- [Skill 语义冲突交互 v1](./skill-conflict-v1.md)
- [资料吸收](../data/assimilation.md)
