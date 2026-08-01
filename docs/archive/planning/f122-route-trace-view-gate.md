# F122 Route Trace View 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-OBSERVE、US-ROUTE、US-ENGINE；
- 用户结果：用户选择 Database，打开一次 Route Trace 并复现候选、选择、回退与 locator；
- 标准旅程：Route Traces → Database → 20 笔 summary → 24 个 step → cursor；
- 作用边界：只读脱敏 observation receipt；不记录、不读取正文或 hidden reasoning；
- 上下文预算：32 个 Database、20 个 trace、每次 24 step，全部 cursor 续页；
- 唯一主要结果：Database/Table scoped Route Trace 时间线与 step flow 页面；
- 明确不做：trace capture、retention mutation、prompt、描述、正文、模型输出与成本；
- 开工前结论：PASS。

## RED 清单

- Route Traces 仍是不可打开占位，bundle 没有 trace module/route；
- scoped Gateway 使用无 `IN DATABASE` 的全局查询而被拒绝；
- trace/cursor 被拼入 MSQL，timeline/step 超预算、混 snapshot 或重复；
- step result 没有 Database/Table scope，深链路无法验证却仍生成 Row/Route link；
- 空 candidate/locator 非 nullable list 被编码成 `null`；
- unknown status/operation/outcome、错 ordinal/ID/scope 或超 24 项仍展示；
- 页面展示或请求 prompt、description、Row values、reasoning、token/cost 或 mutation。

RED 命令：

```text
go test ./internal/adminui -run 'TestEmbeddedBundleHasFrozenOfflineAssets|TestRouteTraceViewModule'
go test ./internal/msql/executor -run TestShowRouteTraceExposesStableScopeAndNonNullLists
```

RED 已证明 bundle 仍只有八个 asset、Route Traces 无链接/module，且 step scope/non-null list
契约不存在而失败。

## 完成门

- module/columns/parameters/scope/page/state 静态契约与真实 Gateway 集成；
- 真实 binary/daemon/Gateway 浏览器覆盖 Database→timeline→step flow、两级续页与状态；
- `scripts/ci.sh` 全绿，bundle hash、设计文档和规划同步；
- 证据满足前完成结论保持 `INCOMPLETE`。

## 完成证据

- RED 先证明第九个 bundle asset、Route Trace route/module 和 step scope/non-null list
  均不存在；随后只增加本 Feature 所需协议补强与页面；
- 静态契约与真实 Gateway 集成覆盖 Database scope、参数化 trace/cursor、13 列 step、
  non-null candidate/locator、20/24 固定预算与只读边界；
- 当前源码构建的真实 binary、daemon、Gateway 与 WebKit 完成 Database→20/21 trace
  timeline→24/26 step 两级分页，Route/Row 深链路、Back/Forward 与 reload 均通过；
- empty scope、missing receipt、permission、invalid path/corrupt 状态通过；1440×1000 视觉检查
  与截图后的独立 reload 零 page/console error 通过；
- bundle hash、race、全仓 CI 与文档同步通过后，完成结论为 `PASS`。
