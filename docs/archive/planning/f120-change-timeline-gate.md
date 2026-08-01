# F120 Change Timeline 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-OBSERVE、US-HISTORY、US-ENGINE；
- 用户结果：用户选择一个 Database，按事务顺序浏览变化并按需展开对象 entry；
- 标准旅程：Changes → Database → 20 笔固定快照 summary → 一笔事务的 32 个 entry；
- 作用边界：只读 committed change metadata/locator，不读取 Row 正文或执行 diff；
- 上下文预算：32 个 Database、20 笔事务、每次 32 个 entry，全部 cursor 续页；
- 唯一主要结果：Database scoped commit timeline 页面；
- 明确不做：retention、倒序读取协议、正文、diff、Trace、mutation；
- 开工前结论：PASS。

## RED 清单

- Changes 仍是不可打开的占位文本，bundle 没有 change module 或稳定路由；
- scoped Gateway 使用无 `IN DATABASE` 的全局查询而被拒绝；
- 页面一次展开全部事务 entry，或 timeline/entry 超预算、混 snapshot、重复；
- transaction/cursor 被拼入 MSQL，跨 Database/transaction cursor 仍展示；
- 单库 timeline 暴露跨库事务中其他未授权 Database ID；
- unknown column/object kind/operation、scope 错配或 Row values 被静默展示；
- 页面顺手执行 SELECT/History、AS OF、diff、Trace 或 mutation。

RED 命令：

```text
go test ./internal/adminui -run 'TestEmbeddedBundleHasFrozenOfflineAssets|TestChangeTimelineModule'
go test ./internal/msql/executor -run TestScopedChangeTimelineRedactsOtherDatabaseIDs
```

当前应因 bundle 仍只有六个 asset、Changes 无链接、change module 与单库 scope redaction
测试能力不存在而失败。

## 完成门

- module/columns/parameter/scope/page/state 静态契约与真实 Gateway 集成；
- 真实 binary/daemon/Gateway 浏览器覆盖 Database→timeline、timeline/entry 续页与状态；
- `scripts/ci.sh` 全绿，bundle hash、设计文档和规划同步；
- 证据满足前完成结论保持 `INCOMPLETE`。

## 完成证据

- RED 已先证明 Changes 仍是占位、bundle 只有六个 asset，并复现单库 summary 暴露
  `db_other` 与全 envelope entry count；
- GREEN 将 scoped `database_ids`/`entry_count` 收敛到所选库，并把空 non-nullable
  `related_object_ids` 确定编码为 `[]`；executor、Gateway 与 bundle 测试覆盖该契约；
- 真实发行 binary、daemon 与 WebKit 已覆盖 Database 选择、20→22 笔 timeline cursor、
  35 个 entry 的 32→35 cursor、深链路、Back/Forward、空 scope、permission 与 corrupt；
- 1440×1000 视觉检查通过；独立 reload 为零 page/console error；
- race、bundle 完整性、全仓 CI 与文档同步通过，完成结论为 `PASS`。
