# F118 Route Tree Browser 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-ROUTE、US-OBSERVE、US-COLD、US-ENGINE。
- 用户结果：用户从 Table 进入语义索引，逐层展开 branch，最终看到 leaf locator。
- 标准旅程：Catalog Table → Table roots → branch children → leaf locators。
- 作用边界：只投影 Router node/membership locator；不读取 Row 正文或改变任何对象。
- 上下文预算：point node + 当前层 12 children，或当前 leaf 20 locators；cursor 续页。
- 永久边界：无 Store API、整树加载、Route 写入、Row SELECT、Vector/predictor 或全文。
- 唯一主要结果：按 stable-ID URL 浏览一个 Table 的 Route Tree。
- 明确不做：Row document、History、Change、Diff、Trace、reshape 和 candidate 合并。
- 开工前结论：PASS。

## RED 清单

- Admin 只有 Route Tree 占位，Catalog Table 无法进入真实 Router；
- branch/leaf 不分类型，页面误用 `SHOW UNDER`/`OPEN` 或读取完整树；
- Route ID 被拼进 MSQL，node/locator scope 不匹配仍展示；
- children/locator 超预算、跨 snapshot 续页或最后一页仍标 truncated；
- loading/empty/truncated/permission/error/corrupt 状态缺失；
- locator 顺手 `SELECT` Row 正文，或页面发出 mutation。

RED 命令：

```text
go test ./internal/adminui -run 'TestEmbeddedBundleHasFrozenOfflineAssets|TestRouteTreeModule'
```

当前应因 bundle 仍只有四个 asset，且不存在 Route Tree module/route 而失败。

## 完成证据

- UI RED 先因五 asset/Route module/route 缺失失败；point scope RED 证明
  `DESCRIBE ROUTE` 缺少 Database/Table identity；真实浏览器 RED 又证明无 root Table
  被误报 internal、missing Route 未稳定映射 not_found；
- point read 增加 `database_id/table_id` 供深链路验证，不改变紧凑 `SHOW` Route Frame；
  无 root 返回确定性空 page，missing/deleted point 返回稳定 not_found；
- module/state/query/parameter/scope/route 静态契约与真实 daemon/Gateway 集成通过；
  locator 严格只有 Database/Table/Row/revision，不含正文；
- 当前源码构建的真实 binary/daemon/Gateway 在 WebKit 完成 Catalog Table→13 个 root
  （首屏 12、续页 13 并回到 ready）→branch→leaf locator、deep-link reload、
  Back/Forward、empty、permission、error 和 cross-Table corrupt；视觉检查通过，最终
  reload 无 console/page error；
- `scripts/ci.sh` 全绿，bundle hash、设计文档和规划同步；完成后结论：PASS。
