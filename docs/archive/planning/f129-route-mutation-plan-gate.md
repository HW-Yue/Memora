# F129 Route Mutation Plan 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

AI 通过只读 MSQL 提交局部 Route split/merge/move proposal；引擎对当前 Table Router
做有界重验并返回确定、可审阅、不可执行的 Route Mutation Plan。

## 产品门

- 语义边界、命名和分组来自 proposal，不由引擎猜测；
- 计划绑定 Table、Route/locator revision、provenance 和 snapshot hash；
- 缺失、重复、跨 scope、截断、超容量、循环或 sibling 冲突全部拒绝；
- F129 绝不写 Route、membership、Row、History 或 Change Log；
- legacy/native backend 共享同一 Source contract 和计划字节。

结论：PASS。

## RED 清单

1. MSQL 无法解析/绑定 `PLAN ROUTE MUTATION ... USING :proposal`；
2. SPLIT 接受 child/Row 分组不完整、重复或混用；
3. MERGE 接受非 sibling、异 kind、root 或容量溢出；
4. MOVE 接受 leaf parent、同 parent、跨 scope 或 descendant cycle；
5. revision、scope、name/alias 冲突或 locator cursor 截断后仍出计划；
6. input shuffle 导致 plan/hash 漂移，或计划泄露 Row values；
7. 计划生成改变任何 Route/membership 或可被 F129 直接执行；
8. native daemon、Parser golden、Canonical Skill 与双 adapter 漂移。

## 明确不做

- 不执行、批准、持久化或自动恢复计划；
- 不生成 Schema 或 Row content mutation；
- 不用模型、向量、倒排或 Route trace 推断分组；
- 不实现跨 Table/Database Route 移动。

## 完成门

- reference model、shuffle、boundary、snapshot conflict、truncation 与 no-write 测试全绿；
- Parser/Executor、legacy/native daemon 和 Skill 派生全绿；
- targeted race 与 `./scripts/ci.sh` 全门通过；
- 规划推进 F130 Route Plan Execution。

## 完成证据

- `go test ./internal/routemutationplan ./internal/msql/parser ./internal/msql/executor ./internal/msql/readquery ./internal/cli ./internal/daemon`；
- `go test -race ./internal/routemutationplan ./internal/msql/executor ./internal/daemon`；
- native daemon 完成真实建库、建表、建 Route、写 membership、生成 plan 和 no-write 复查；
- canonical Skill 通过 `quick_validate.py`，双 adapter 由生成器确定性更新；
- `./scripts/ci.sh` 全门通过。
