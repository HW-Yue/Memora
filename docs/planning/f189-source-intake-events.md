# F189：Source Intake 交互与即时事件

状态：已批准，2026-08-05 开工。

## 唯一主要结果

冻结整本资料进入长任务前的 Agent-owned Source Intake 状态机和版本化事件协议。收到结构清单后，
必须先输出 inventory、询问“全部还是选定范围、写入哪些 Database”，并进入 waiting_user；解析器或
后续调度发现问题时，也能立即输出 issue/question/waiting，收到用户回答后恢复。

F189 只做内存状态与纯结构事件，不持久化 Job、不保存原文、不解析 EPUB/PDF、不调用模型或 MSQL。
F190 才把 Command/Event/checkpoint 持久化并支持崩溃恢复。

## 固定旅程

```text
Begin(inventory)
→ inventory_ready → question(scope) → waiting_user
→ Confirm(all | selected units, target databases)
→ intake_accepted
→ Progress(...)*
→ Issue(..., requires_user=true)
  → issue → question(issue) → waiting_user
→ Answer(question_id, short value)
  → answer_received（只含 value SHA-256）→ intake_resumed
→ Cancel 可从 waiting/accepted 进入 cancelled
```

每个调用同步返回一个有序事件 batch；调用方可以逐个写入 CLI、stdio 或未来 daemon stream，因此
问题和错误不需要等任务结束。batch 生成失败时状态与 sequence 不变化。

## 边界

- Inventory 只含 source ID/title/locator/content hash 和结构 Unit；没有 content/body/text/chunk 字段；
- Unit 有稳定 ID、parent、kind、label、可读 extent 和 optional；F192 才升级为完整 Document IR；
- selection 必须引用 inventory Unit，`all` 与 selected IDs 互斥，target databases 非空且去重；
- Session 同时最多一个 outstanding question；question ID 与 answer 严格匹配；
- answer value 只返回给当前调用者，事件仅保存 UTF-8 byte count 和 SHA-256；
- progress 只接受单调 counters；accepted 前不能进度，cancelled 后任何命令 fail closed；
- event 只有与 kind 对应的 payload，Validate 拒绝正文形字段、错序、错 task 或错 state；
- Snapshot 深拷贝，不能通过调用者篡改恢复状态。

## RED 与完成门

- RED 先证明 Intake Session/Event/Inventory/Selection 尚不存在；
- Begin 固定输出 inventory→question→waiting，事件逐项 Validate 且 JSON 不含原文；
- all/selected、越界 Unit、重复 Database、错 question answer 和重复 Begin fail closed；
- issue 立即进入 waiting，answer event 只含 digest，随后可继续单调 progress；
- cancel terminal、并发调用 one-winner、Snapshot 深拷贝与 race 通过；
- Agent import allowlist 与完整 CI 全绿。

用户执行授权：2026-08-05 用户要求持续执行至 F204。

开工前结论：PASS。
