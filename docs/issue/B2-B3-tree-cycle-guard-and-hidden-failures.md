# B2 + B3 · 环与坏路径的可观测面（一块做）

严重度：medium-high。状态：**待做**。

## 症状

1. **B2**：一份自洽的手写路由计划能把**环**落盘 —— 第二道门不查环，执行期也不查环/不查 kind。
   之后父链遍历没有 hop 保护 → `RECALL` / `DELETE` 死循环，而 `doctor` 只报 healthy。
2. **B3**：位置解析失败被静默吞掉 —— 这是"树已损坏被掩盖"的那一面。

## 证据

- 不查环：`internal/routemutationplan/validate.go:74-81`；执行期 `route_plan.go:253-301`
  （`checkTreeShape` 不查 kind、不查环）。
- 无 hop 保护：`internal/sqlstore/recall_read.go:99-113`、`internal/sqlstore/archive.go:23-46`
  （`withPath` 有 64 跳上限，唯独这两处没有）。
- 静默吞掉：`recall_read.go:77-80`、`recall_vector.go:96-99` 直接 `continue`；
  `query.go:415` 对 `node.Path == ""` 也 `continue`，而 `:179` 无条件把 `route_paths` 设成空数组。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) B2、B3。

## 已定的修法

1. Validate 补环检测（复用 `schemachangeplan/build.go:345-347` 的 `descendant` 或等价判定）。
2. 那两处父链遍历加**同样的 hop 上限**，并且**不再静默 `continue`**。

**B3 不单开分支**：单独"补错误处理"只是把静默换成噪音，它必须和环防护一起做，成为"树坏了能被看见"的那一面。
