# F197：问题驱动的分支暂停与恢复

状态：实现中；2026-08-05 规格已冻结。

## 唯一主要结果

长资料处理发现必须由用户决定的歧义时，立即产生可持久的 issue/question 事件，仅暂停关联
extent/claim 分支；其他分支仍可继续阅读和草拟。用户选择以幂等、版本化 command 记录，重启后
可恢复该分支，不丢 coverage、checkpoint 或 F196 claim ledger。

## 与旧全局等待的边界

- F189 `SourceIntake` 的初始 scope 确认仍是全局 `waiting_user`，它在用户选择资料范围前
  本来就不能开始处理；
- 解析、draft 或后续 review 期间的局部问题使用新的 branch command，不再把 Job 整体
  切换为 `waiting_user`；
- branch 是 Agent-owned 调度标识，与 Database Route Branch 无关，不进入 MSQL 或用户数据库。

## 命令、事件与快照

`AssimilationJobCommand` 增加：

- `raise_branch_issue`：携带 branch ID、extent sequence/digest、可选 claim ID、issue ID/code/message、
  有界 question 和 2–8 个稳定 option；
- `answer_branch_issue`：携带 branch/issue ID、所选 option、回答 UTF-8 字节数和 SHA-256，
  不持久化自由文本回答正文。

两者都使用现有 command ID 幂等与 hash-chain journal，并增加 `expected_branch_revision`。
Job event 仍有全局单调 revision，但 branch command 只对目标 branch revision 做乐观并发校验；
其他分支事件不会使当前用户回答过期。

snapshot 保存按 branch ID 规范排序的有界分支状态：

- `waiting_user`：当前 issue/question 存在，该 branch 不可调度；
- `ready`：用户选择已持久化，保留最后 resolution，该 branch 可恢复；
- 未出现在 branch snapshot 中的分支默认 ready。

Job 处于 active 时，一个或多个 branch waiting 不改变 Job 全局状态。取消的 Job 没有可运行分支；
初始 scope 尚未确认的 Job 也不允许创建 branch issue。

## 不变量

- issue 必须绑定一个真实 extent digest；claim ID 去重且有上限，具体存在性由编排器
  与 F196 ledger 在发 command 前确认；
- 同 branch 同时只有一个 active issue；回答必须精确匹配 issue 和预先给定 option；
- raise/answer 不改变 Source Intake、coverage checkpoint、已有 claim 或其 digest；
- 同 command ID 同内容重放原 receipt，同 ID 换内容冲突；同 branch revision 的并发回答
  只有一个胜出；
- journal 不保存用户自由文本回答；选中的稳定 option 是调度决议，可持久化。

## TDD 与完成门

RED 先锁定：局部 issue 后 Job 仍 active、未受影响 branch 可调度、目标 branch 等待；
其他分支推进后原 answer 仍可以 branch revision 提交；reopen 恢复问题与 resolution；checkpoint/
intake 逐字节不变；非法 option/issue/revision 拒绝；并发回答单赢家；journal 无私密回答正文；
reopen/corruption、race 和全量 CI 全绿。

F197 不调用 Provider、不修改 F196 claim 内容、不做 F198 语义复核，也不提交 F195 plan。

## 关联

- [F190 可持久 AssimilationJob](./f190-durable-assimilation-job.md)
- [F196 Draft / Claim Ledger](./f196-draft-claim-ledger.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
