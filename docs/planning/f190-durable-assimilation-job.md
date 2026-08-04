# F190：可持久恢复的 AssimilationJob

状态：已完成，2026-08-05。

## 唯一主要结果

在 Agent 边界建立内容无关的 append-only AssimilationJob journal，使 F189 Source Intake 的事件、
版本化 Command 和 coverage 前置 checkpoint 可在进程退出后恢复。相同 Command 重试返回相同结果，
不同 Command 并发更新只能有一个 revision 获胜。

F190 不保存源文件或正文、不解析文档、不调用 Provider/MSQL，也不把 Job 写进用户 Database。
F191 才提供临时 SourceStore，F194 才解释 coverage checkpoint，F195 才增加正式 MSQL 提交面。

## 协议

```text
start(initial F189 inventory/question/waiting events, expected_revision=0)
→ revision 1 / waiting_user

append_source_events(expected_revision=N)
→ revision N+1 / waiting_user | active | cancelled

save_checkpoint(expected_revision=N, source_sequence=current)
→ revision N+1 / state unchanged

cancel(expected_revision=N, reason_code)
→ revision N+1 / cancelled
```

- Command 使用稳定 `command_id`；同 ID、同规范摘要为 replay，同 ID、不同摘要为冲突；
- `expected_revision` 在首次执行时实现 one-winner；幂等 replay 不因 revision 已推进而失败；
- Source event batch 必须逐项通过 F189 `Validate`，task、sequence 和允许的 batch 形状连续；
- 每次 revision 同时重建可验证的 F189 Intake Snapshot；reopen 后可恢复 Intake 并从下一 sequence 继续；
- checkpoint 只含 ordinal、当前 Source sequence、stage、cursor 和 state SHA-256，不含正文；
- journal record 固定 previous-record hash、command hash 和 record hash；每次 append 后 `fsync`；
- 最后一个没有换行的 torn record 可安全截断；完整换行但 JSON/hash/chain 损坏时 fail closed；
- 路径由 JobID SHA-256 派生，调用者输入不能逃逸根目录；根目录权限固定为 owner-only；
- v1 只承诺单个 Store 实例内并发安全；多进程任务租约不在本 Feature 偷渡。

## RED 与完成门

- RED 先证明 Store、Command、Event、checkpoint 和恢复 API 不存在；
- start→accept→progress→checkpoint 后关闭并 reopen，Snapshot/Event history 完整一致；
- 同 Command 重试不追加 record；同 ID 异内容、旧 revision 和 terminal 后更新 fail closed；
- 16 路同 revision 竞争只有一个成功，`-race` 通过；
- torn tail 自动恢复并允许后续 append；完整 checksum/chain corruption 拒绝读取；
- journal JSON 不出现测试源正文或回答正文，回答仍只有 F189 digest；
- Agent import allowlist、完整 CI 和双架构 build 全绿。

用户执行授权：2026-08-05 用户要求持续执行至 F204；真实模型限速不影响本确定性 Feature。

## 完成证据

- start、Source event append、checkpoint 和 job-level cancel 均写入 hash-chain journal 并 `fsync`；
- reopen 后恢复 inventory、selection、question、progress、sequence 和 checkpoint，恢复的 F189 Session
  能继续生成并持久化下一批进度事件；
- 同 Command replay 不增加 revision，同 ID 异内容冲突，16 路同 revision 更新只有一个成功；
- 非首条 torn tail 截断到最后完整 record；首条 torn record 回滚为未创建，随后可安全重试 start；
- 完整 JSON record 的 revision/checksum/previous hash 被修改后 fail closed；
- 测试证明用户回答正文和伪造源正文不进入 journal，目标测试与 `-race` 全绿；
- Store 只使用标准库，路径由 JobID SHA-256 派生，目录/文件权限分别为 `0700`/`0600`。

完成门结论：PASS。F197 在不破坏旧 journal 摘要的前提下，以可选字段增加了
branch-local issue/answer/revision；完整契约见 [F197](./f197-branch-pause-resume.md)。
