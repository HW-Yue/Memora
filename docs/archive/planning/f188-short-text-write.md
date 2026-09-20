# F188：单网页/短文本写入垂直链

状态：已完成，2026-08-05。

## 唯一主要结果

交付第一条内置 Agent 短资料写入链：对当前已读取的单网页或短文本做一次有界 Bootstrap 和一次
Provider Tool Call，生成单 Row MSQL proposal；用户审批后通过 F187 Write Gateway 原子提交，再按
真实 RowID 用独立只读 MSQL `SELECT` 回读验证。

F188 不下载网页、不解析文件、不创建 Job/SourceStore、不保存原文 chunk、不创建 Schema/Route，
也不自动批准。输入正文只存在于当前 Provider 调用；持久化内容只能是用户审批的完整语义模块。

## 固定旅程

```text
ShortTextSource（title/content/locator + 查询意图）
→ MSQL Bootstrap（Atlas + lexical + 可选 root prefetch）
→ Provider required tool：propose_short_write（恰好一次）
  → target Database/Table、单条参数化 MSQL、Schema version、Route Leaf、reason
→ runtime 派生 source kind/locator/content SHA-256/max affected rows
→ F187 Prepare → proposal + 脱敏 Trace
→ 等待用户审批（零写调用、无事务）
→ F187 ExecuteApproved（单 statement、单 Row autocommit）
→ 从 INSERT result 取得真实 RowID/revision/commit sequence
→ 固定 SELECT * WHERE row_id=:row LIMIT 1
→ verified receipt
```

## 边界与失败语义

- source content 默认上限 32 KiB；Bootstrap Frame、Provider output、tool arguments 和总调用数均硬限制；
- Provider 只有 `propose_short_write`，不能直接调用 MSQL；Tool 参数严格 JSON 解码，未知字段失败；
- v1 proposal 恰好一个 statement、`max_affected_rows=1`、`expected_revision=0`；runtime 覆盖 actor、
  source、SourceKind、locator 和 content hash，模型不能伪造来源；
- 目标 Database 必须在 Write Profile scope，Table/Database 只作为后续固定 SELECT 的 quoted identifier；
- proposal 原文会展示给用户并由 F187 digest 绑定；L2/L3 仍由引擎拒绝；
- commit 结果必须是单个成功 `INSERT` 且返回一个 RowID。其他结果不生成成功收据；
- INSERT 是单 Row autocommit，不存在多 Row 半提交。commit 成功但回读失败时返回
  `committed_unverified`，不得假称回滚；F199 才处理长任务 reconciliation；
- SELECT 回读的 RowID、revision、commit sequence 必须与 INSERT receipt 一致；
- Draft Trace 只保存摘要和 SHA-256，不保存正文、MSQL、Row、答案或 Key。

## RED 与完成门

- RED 先证明 ShortText Agent、strict tool contract、plan/receipt 尚不存在；
- scripted Provider 证明 Bootstrap 后只调用一次模型，proposal 等待审批期间没有写调用；
- malformed/multiple/wrong tool call、超长正文、越 scope target、无 guard 全部 fail closed；
- 正确审批后 INSERT→真实 RowID→SELECT 完整通过，receipt 可验证；
- commit 失败不发 SELECT；回读失败明确返回 committed_unverified 和 commit evidence；
- 真实 MSQL Service 证明单 Row 写入、Route membership 和 SELECT 回读一致；
- race、Agent import allowlist 与完整 CI 全绿。

用户执行授权：2026-08-05 用户要求持续执行至 F204；真实模型限速时一次成功链路证据即可。

## 完成证据

- RED：ShortText Agent、strict Tool contract、Plan 与 Receipt 均不存在，单元/Service 测试编译失败；
- GREEN：一次 Bootstrap + 一次 required Tool Call 生成单 Row proposal；等待审批期间只有只读
  Bootstrap，没有写调用；
- 正文超限、直接回答、错误 Tool、未知字段、越 scope 和缺 Schema guard 全部 fail closed；Draft
  Trace 不含正文、MSQL 或 Row；
- 获批后单 INSERT 返回真实 RowID/revision/commit sequence，再以固定 SELECT 回读并逐项对拍；
- commit 失败不发 SELECT；回读失败返回带 commit evidence 的 `committed_unverified`；
- 真实 MSQL Service 验证 Row、Route Leaf membership 与 SELECT 回读一致；race、import allowlist 和
  完整 `scripts/ci.sh` 全绿。

完成结论：PASS（scripted Provider；真实大批量模型质量仍按用户决定延期）。
