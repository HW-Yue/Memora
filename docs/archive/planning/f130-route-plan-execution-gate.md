# F130 Route Plan Execution 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

原子执行一个经过显式 hash-bound approval 的 Route Mutation Plan，并返回完整覆盖收据。

## 产品门

- 使用 `APPLY ROUTE MUTATION PLAN :plan FOR TABLE ...`，不开放物理或 ad hoc action API；
- approval 同时绑定 action、plan hash、actor 与 Database scope；
- 同一 authority write window 重验 node/child/locator guards；
- Route、membership 与 Change Log 在一个 native transaction 内发布；
- stale、tampered、跨 scope、无批准、staging failure 全部零部分写入；
- branch reparent 对完整子树 guard 并确定性更新 descendant path。

结论：PASS。

## RED 证据

1. Parser 不认识 `APPLY ROUTE MUTATION`，Executor 无执行路径；
2. 计划没有自校验，branch split/merge 漏 guard 后代；
3. native repository 禁止 reparent，也无法把 membership 指向同事务新 leaf；
4. 无批准、tampered/stale plan 缺少统一零写保证；
5. daemon 没有真实 plan→approval→apply→reopen 旅程。

## 完成证据

- plan identity/hash、subtree guard、approval 与静态 Table scope 测试；
- split 原子提交、stale/no-approval 零写、Change Log 与 reopen 测试；
- native daemon 完成真实建库、建表、写 Row/membership、plan、apply 和目标 leaf 复查；
- Canonical Skill quick validation 与双 adapter 确定性生成；
- targeted race、全仓测试、integration/e2e 与双架构构建门全绿。

## 明确不做

- 不生成 Schema migration，不引入自动 rebase 或跨库 Route；
- 不持久化可绕过 snapshot guard 的 replay token；
- 不推断任何 child/Row 分组。

下一项：F131 Schema Change Plan。
