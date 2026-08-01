# F114 Trace Read Protocol 开工与完成门

状态：已完成；RED、GREEN、故障矩阵、daemon/reopen/prune 与完整 CI 均通过。

## 唯一主要结果

宿主提交的 bounded Route navigation receipt 可清理，并可通过 scope-safe MSQL timeline
和 step page 读取；正文、prompt 与隐藏推理不进入 trace。

## RED

- 已确认 Parser/Executor 不认识 `SHOW ROUTE TRACES/TRACE`；
- 没有 canonical trace envelope、sequence、ID locator 或 retention epoch；
- 新 trace 混入 continuation，prune 后旧 cursor 静默漏页；
- scoped reader 可枚举其他 Database，或 timeline 泄露 steps/正文；
- daemon 无真实 record/read/reopen/prune 路径。

## 完成门

- envelope codec、budget/tamper、atomic record/idempotency、reopen、corruption 与 race；
- timeline/step cursor tamper、scope、snapshot、新 append 和 prune epoch；
- authorization、summary 无 steps、step 无 prompt/descriptions/Row values；
- real daemon record/read/reopen/prune、完整 CI；
- 独立提交并快进合入 `main`。

## 明确不做

- Admin 页面、Change 事件、Security Audit、自动捕获 hidden reasoning、模型成本或
  predictor provenance。

## 完成证据

- canonical envelope/meta codec、未知字段/tamper、硬预算和 stable ID grammar；
- body+ID locator+high-water 原子提交、幂等 retry、commit fault 零残留与 32 writer race；
- reopen、body/locator corruption 失败关闭、expiry prune 原子推进 retention epoch；
- timeline 固定 high-water，新 append 不混页，prune 后旧 cursor 返回 revision conflict；
- timeline/step cursor tamper、跨 scope、跨 trace、非 canonical 与参数边界；
- scoped authorization、summary 无 steps、step 无 prompt/description/Row values；
- 真实 daemon record/read/reopen/prune 与错误 scope record 拒绝；
- `scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、e2e、cross-build 全绿。
