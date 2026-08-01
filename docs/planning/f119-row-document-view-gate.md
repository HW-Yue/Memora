# F119 Row Document View 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-READ、US-OBSERVE、US-HISTORY、US-ENGINE。
- 用户结果：用户从 leaf locator 打开 RowID，阅读完整动态文档并看到 revision 时间线。
- 标准旅程：Route leaf locator → current point Row → 20 条 History metadata。
- 作用边界：只读一个 Row 当前值与自身 History；不读取其他 Row 或修改对象。
- 上下文预算：一个 point Row + 20 条 revision metadata；History cursor 续页。
- 永久边界：无 Store API、字段名猜测、横向 Grid、全表 scan、mutation 或 HTML 投影。
- 唯一主要结果：Data Dictionary 驱动的 Row document 页面。
- 明确不做：historical body、revision diff、relation、change timeline、Route trace。
- 开工前结论：PASS。

## RED 清单

- locator 仍是不可打开的文本，Admin 没有 Row stable-ID route/module；
- 页面猜 title/body 或只展示固定列，动态 Schema 字段丢失/乱序；
- RowID 拼入 MSQL，row_detail/Table/Row scope 错配仍展示；
- History 泄露 values、超 20 条、跨 snapshot 混页或续页后仍 truncated；
- missing current Row 与空 History、permission/error/corrupt 状态混淆；
- 页面顺手执行 AS OF、diff、relation、mutation 或全表扫描。

RED 命令：

```text
go test ./internal/adminui -run 'TestEmbeddedBundleHasFrozenOfflineAssets|TestRowDocumentModule'
```

当前应因 bundle 仍只有五个 asset，且 locator 无链接、Row module 不存在而失败。

## 完成门

- module/dictionary/parameter/scope/state 静态契约与真实 Gateway 集成；
- 真实 binary/daemon/Gateway 浏览器覆盖 locator→document、动态字段、History 续页与状态；
- `scripts/ci.sh` 全绿，bundle hash、设计文档和规划同步；
- 证据满足前完成结论保持 `INCOMPLETE`。

## 完成证据

- RED 已先证明 bundle 仍只有五个 asset、locator 不可打开且 Row module 不存在；
- 静态与 Gateway 集成测试覆盖严格 result/Row Detail contract、参数化 RowID、动态字段、
  History 14 字段及 20 条页面预算；
- 真实发行 binary、daemon 与 WebKit 已覆盖 locator 深链路、四个动态字段、字面 HTML
  文本、21 条 History 的 cursor 续页、deleted current Row、permission 与 corrupt 状态；
- 1440×1000 视觉检查通过；清空截图工具噪声后独立 reload 与 History 续页均为零
  page/console error；
- race、bundle 完整性、全仓 CI 与文档同步通过，完成结论为 `PASS`。
