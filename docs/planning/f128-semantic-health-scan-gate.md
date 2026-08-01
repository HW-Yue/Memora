# F128 Semantic Health Scan 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

Semantic Health v2 对当前 Catalog、live Row、Table Router 和 locator 做有界一致快照扫描，
确定性发现 Route 拥挤/歧义、结构断裂、错 scope、陈旧/孤儿 membership、未路由 Row 与
既有 Schema 债务。

## 产品门

- 只报告可由结构和 revision 证明的问题，不由引擎猜测语义正确归类；
- 报告不返回 Row 正文，只返回稳定 locator、计数和 review action；
- 任一 Row/Route/locator 扫描截断时显式 `truncated`，不产生依赖缺失后缀的误报；
- 所有新增 issue 均为 review-required，F128 不自动 reshape、迁移或删除；
- native 与 legacy 逻辑 backend 走同一只读 Source contract。

结论：PASS。

## RED 清单

入口：`go test ./internal/semantichealth ./internal/daemon ./internal/skillcontract`

1. child ≥12 或 leaf locator ≥100 未报告 capacity；
2. 同 parent 的名称/alias 冲突、断 parent、root 数异常未报告；
3. live Row 无 membership、locator scope 错、Row 不存在或 revision 陈旧未报告；
4. Row/locator 截断后把未扫描对象误报 orphan/unrouted；
5. issue ID/顺序/hash 随输入顺序变化，或报告泄露 Row values；
6. 新 issue 被标为 auto-fix，或维护请求能直接执行语义修复；
7. native daemon、Canonical Skill 与 Semantic Health version 漂移。

## 明确不做

- 不判断某 Row 应归入哪个语义叶子；
- 不生成或执行 split/merge/move 计划；
- 不读取 Route trace 作为优化收益证据；
- 不恢复已撤销的 pending-reindex 自动修复声明。

## 完成门

- reference fixture、input shuffle、truncation、revision/scope 和 native daemon 测试全绿；
- Semantic Health v2、Skill contract 与双 adapter 确定性派生全绿；
- targeted race 与 `./scripts/ci.sh` 全门通过；
- 规划推进 F129 Route Mutation Plan。

## 完成证据

- `go test ./internal/semantichealth ./internal/daemon ./internal/skillcontract ./internal/codexadapter ./internal/claudeadapter`；
- `go test -race ./internal/semantichealth ./internal/daemon`；
- canonical Skill 通过 `quick_validate.py`，双 adapter 由生成器更新；
- 原生 daemon 实际启动 Page Store authority，并通过 `memora.semantic-health/v2` 报告路径；
- `./scripts/ci.sh` 全门通过。
