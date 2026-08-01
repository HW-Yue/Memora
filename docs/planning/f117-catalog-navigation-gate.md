# F117 Catalog Navigation 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-OBSERVE、US-COLD、US-ENGINE。
- 用户结果：用户可从授权 Database 逐层浏览 Table 和完整动态 Schema，不读 JSON。
- 标准 MSQL 旅程：`SHOW DATABASES` → `DESCRIBE DATABASE` + `SHOW TABLES` →
  `DESCRIBE TABLE` + `SHOW COLUMNS`，全部 `LIMIT 32 COMPACT`。
- 作用边界：只投影 Catalog；不改变对象、revision、Route membership、Row 或 transaction。
- 上下文预算：一次只读当前层与 32 个 children；cursor continuation 不混 snapshot。
- 永久边界：无 Store API、名称猜测、正文、Vector、mutation 或全 Catalog prompt。
- 唯一主要结果：Instance→Database→Table→Schema 可浏览。
- 明确不做：Route Tree、Row document、Change、Diff、Trace 页面。
- 开工前结论：PASS。

## RED 清单

- `/catalog` 仍只有占位壳，无法从真实 result envelope 展示层级；
- display name/URL 可注入 MSQL 或 HTML，stable ID rename 后深链路失效；
- 一次无界加载或 cursor 跨层复用，revision conflict 混合两份 snapshot；
- loading/empty/truncated/permission/error/corrupt 任一状态缺失或泄露内部错误；
- Back/Forward、刷新深链路、session 恢复或真实 Gateway journey 失败；
- 页面顺手读取 Route/Row 或发出 mutation。

RED 命令：

```text
go test ./internal/adminui ./internal/adminapi
```

当前失败应由 catalog module/route/状态投影尚不存在导致。

## 完成证据

- RED 先因 Catalog module/route 缺失而失败；真实 Gateway RED 又暴露 indexed reader
  只能按 Table name 解析，随后补齐 Database/Table stable-ID point lookup 与 scope/
  locator/body corruption 检查；
- bundle/state/query/escaping/route 静态契约，以及真实 daemon 上按 stable ID 执行
  `DESCRIBE` + 相邻 `SHOW` 的 Gateway scope 集成测试通过；
- 用当前源码构建真实 binary，启动真实 instance/daemon/Gateway 后在 WebKit 完成
  Instance→Database→Table→Schema、deep-link reload、Back/Forward、permission/error；
  33 个 Table 首屏严格为 32，cursor 后为 33 且状态由 truncated 回到 ready；空
  Database 显示 empty；最终页面无 console/page error，视觉检查通过；
- 严格 envelope 校验拒绝错误 stable ID、父 Database、schema/page 字段与跨 snapshot
  continuation；无 HTML 注入、Web Storage、mutation、Route/Row/正文读取；
- `scripts/ci.sh` 全绿，bundle hash、文档和规划同步；完成后结论：PASS。
