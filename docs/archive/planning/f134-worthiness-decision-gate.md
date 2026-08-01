# F134 Worthiness Decision 开工门

状态：已完成（2026-08-01）。

## 单一主要结果

AI 把一个仍为 pending 的 Host Input 终结为 IGNORE、WRITE 或 REVISE，并取得稳定、
可重放、可跨 daemon restart 恢复的 decision receipt。

## 产品门

- 协议为 `memora.worthiness-decision/v1` 与 `memora.worthiness-receipt/v1`；
- decision 必须绑定原 input/content-independent receipt 的 input hash、scope hash、workspace
  和完全相同的授权 Database 集合；
- IGNORE 绑定已完成 preflight 的 ignored Mutation Receipt；
- WRITE 只接受 committed + verified INSERT receipt，REVISE 只接受 committed + verified
  REVISE receipt；已提交但未验证的 mutation 不能终结候选；
- WRITE/REVISE 目标必须位于授权范围，并与 mutation change 的 Row ID/revision 对应；
- 成功后原子移除 pending 正文、释放 inbox 容量并保存不含正文的稳定 decision record；
- 同 decision 重试返回 replay；同 input 异 decision 或 decision ID 复用返回 revision conflict；
- 引擎只验证绑定和证据形状，不替 AI 判断语义价值，也不在 decision API 内执行 MSQL。

## RED 清单

1. 尚无版本化 decision/receipt、三种 verdict 或严格形状校验；
2. decision 可脱离 capture hash、workspace 或授权范围；
3. WRITE/REVISE 可凭伪成功、未验证或错误 decision 的 Mutation Receipt 终结候选；
4. target 与 mutation change 不一致仍成功；
5. decision 成功后 pending 未原子移除，或容量未释放；
6. 同 input/decision ID 可产生互相冲突的多个结果；
7. daemon restart 后 decision receipt 丢失，或 CLI/IPC 接受未知字段；
8. receipt、审计或决策记录泄露 candidate text。

## 明确不做

- 不在 F134 自动发现 Database/Table、生成 Mutation Plan 或执行 MSQL；
- 不保存已终结的 candidate text；恢复只返回 decision 与 receipt；
- 不把 receipt 的结构校验描述成对 AI 语义判断的背书；
- 不扩展 IGNORE/WRITE/REVISE 之外的拆分、合并、移动决策。

## 完成门

三种 verdict、binding、strict decode、idempotency/conflict、capacity release、atomic transition、
reopen、corruption 和 no-content evidence 全绿；targeted race 与全仓 CI 通过后进入 F135。

## 完成证据

- `internal/hostinput` 覆盖 IGNORE/WRITE/REVISE、capture binding、授权目标、Mutation
  change Row/revision 匹配、并发 replay 与 identity conflict；
- commit fault 证明 pending delete、decision record 与 ID index 同事务回滚，corruption
  测试证明损坏 decision fail-closed；
- native daemon 证明终结前后无逻辑 Database 写入，restart 后可恢复稳定 decision；
- CLI/IPC strict decode、Canonical Skill 校验和 Codex/Claude adapter 生成测试通过；
- `go test -race ./internal/hostinput ./internal/daemon ./internal/cli ./internal/skillcontract`
  与 `./scripts/ci.sh` 全绿。

下一项：F135 Scalable Database Discovery。
