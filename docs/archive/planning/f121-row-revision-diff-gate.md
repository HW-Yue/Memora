# F121 Row Revision Diff 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-HISTORY、US-OBSERVE、US-ENGINE；
- 用户结果：用户从 Row History 或事务 entry 打开同一 Row 的 before/after；
- 标准旅程：revision N/N+1 → 两次 AS OF point read → Data Dictionary 字段级比较；
- 作用边界：只读一个 RowID 的两个 revision；不读 current、Route history 或其他 Row；
- 上下文预算：每侧一行，两侧正文合计 512 KiB；
- 唯一主要结果：Row revision pair 的确定性字段 diff 页面；
- 方向修正：Route 没有 MSQL 历史读取协议，不能在页面 Feature 中绕过引擎补做；
- 开工前结论：PASS。

## RED 清单

- bundle 没有 diff module/route，History 与 Row change entry 不能进入比较页；
- revision/RowID 被拼入 MSQL，或缺少 `AS OF REVISION` 的精确 point predicate；
- before≥after、跨 Row/Table、请求 revision 与返回 revision 错配仍展示；
- 页面猜字段名、忽略 Column ID/order，或 Schema/Row Detail 错配仍比较；
- 两侧正文无字节预算，非 scalar 值、HTML 或超预算值静默投影；
- missing/permission/corrupt/over-budget 混成普通空状态；
- 页面顺手读取 Route history、History page、current Row 或执行 restore/mutation。

RED 命令：

```text
go test ./internal/adminui -run 'TestEmbeddedBundleHasFrozenOfflineAssets|TestRowRevisionDiffModule'
```

当前应因 bundle 仍只有七个 asset、diff module/route/link 均不存在而失败。

## 完成门

- module/columns/parameters/identity/budget/state 静态契约与真实 Gateway 集成；
- 真实 binary/daemon/Gateway 浏览器覆盖 History/Change entry→diff、深链路与状态；
- `scripts/ci.sh` 全绿，bundle hash、设计文档和规划同步；
- 证据满足前完成结论保持 `INCOMPLETE`。

## 完成证据

- RED 已先证明 bundle 只有七个 asset，diff module/route、History 与 Change entry 入口
  均不存在；
- 静态与真实 Gateway 集成覆盖两次参数化 AS OF point read、相同 Row/Column/Row Detail
  contract、revision 1→2 和不同正文；
- 真实发行 binary、daemon 与 WebKit 已覆盖 Row History→diff、Change entry→diff、四个
  动态字段的 2 changed/2 unchanged、HTML 字面文本、深链路、Back/Forward 与 reload；
- missing revision、permission、corrupt pair 状态通过；512 KiB 预算与非 scalar 拒绝由
  module 契约冻结；
- 1440×1000 视觉检查及独立 reload 零 page/console error 通过；
- race、bundle 完整性、全仓 CI 与文档同步通过，完成结论为 `PASS`。
