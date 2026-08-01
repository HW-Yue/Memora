# F124d CPU Exact Route Match 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

通过 MSQL 对同一 embedding space 的当前 Route generation 做授权范围内纯 CPU 精确点积，
稳定返回有全局数量/字节预算的 navigation-only Top K 候选。

## 产品门

- AI-native：只预测 AI 维护的 Route 位置，事实仍由 Router + RowID SQL 获取。
- 降级：无模型、无 generation、stale 或错误预测均不阻断正常 Router。
- 确定性：相同 snapshot、space、query 和 scope 得到完全相同排序与 receipt。
- 可替换：SQL/Discovery 契约不暴露 exact backend，后续 HNSW/Accelerate 不能改变语义。

结论：PASS。

## RED 清单

入口：`go test ./internal/routeexact ./internal/msql/parser ./internal/nativerow ./internal/daemon`

1. 精确 Top K 与独立 reference model、并列排序不一致；
2. NaN、Inf、非归一化、错误维度、错误 space 或非法 limit 被接受；
3. 未授权 Database 被打开，Table scope 在取向量/点积之后才过滤；
4. query slice 或返回结果与输入 generation 共享可修改状态；
5. MSQL 缺少显式 SPACE 或任一预算仍能解析；
6. generation unavailable/stale/incompatible 被误报为事实查询失败；
7. native/daemon reopen 无法读取 Instance 派生 generation 或泄露未授权候选。

首次 RED 预期因 `internal/routeexact`、VECTOR AST/MSQL 和 reader 注入接口尚不存在而失败。

## 明确不做

- 不生成或下载 embedding；
- 不实现 HNSW、Apple Accelerate、GPU、量化或持久化 query；
- 不把候选当答案，不读取 Row/History/relation；
- 不让 Agent 管理 generation 文件。

## 完成门

- reference/random、边界、授权先过滤、deep-copy、race 与 real reopen 全绿；
- predictor failure 保持 Discovery/Router 可回退；
- `./scripts/ci.sh` 全门通过；
- 更新规划状态并保留 F162/F163 的证据门。

## 完成证据

- RED：首次 `go test ./internal/routeexact` 因 package 无生产代码失败；native MSQL
  RED 随后因 `executor.NewWithRouteVectors` 尚不存在而失败。
- reference：80 个确定性随机向量、独立点积/排序 reference、Table scope scan count、
  tie-break、输入不变与 generation snapshot 测试通过。
- 边界：NaN、Inf、非归一化、错误维度/space/limit/参数类型全部拒绝。
- 集成：真实 native Catalog/Route + 两库 generation，验证授权库在 `OpenActive` 前过滤、
  reopen 后精确候选、space 不兼容与 stale unavailable 回退。
- daemon：Instance `derived/route-vector-v1` reader 已接线；无 generation 的完整 IPC/MSQL
  请求返回成功的 unavailable Discovery receipt。
- Race：exact、lexical、parser、executor、native、daemon 与 CI guard targeted race 全绿。
- 完整门：`./scripts/ci.sh` 通过 format、vet、unit、全仓 race、integration、e2e 与
  amd64/arm64 cross-build。
