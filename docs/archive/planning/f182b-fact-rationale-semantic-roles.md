# F182b：Fact / Rationale Column Semantic Roles

状态：已完成。

## 唯一主要结果

扩展 Catalog/MSQL Column `ROLE` 的受控词表，使 `fact` 与 `rationale` 能和既有
`title/summary/identity/status` 一样被规范化、持久化和读取。它解除 F182 public fixture 经
MSQL 无损物化到 F183 clean Instance 的最后一个已知 Schema 缺口。

## 语义与边界

- `fact`：可直接用于回答或判断的事实值；
- `rationale`：支持事实、决策或偏好的理由；
- 两者只作为自描述 Column metadata，不改变 Row value 类型、检索排序或显示回退；
- `title` 与 `summary` 继续在单 Table 内各最多一个；`fact`、`rationale` 不设唯一性；
- 大小写继续规范化为小写；未知 role 继续在 WAL/持久化前拒绝；
- 不在本 Feature 修改 F182 corpus、增加任意自由文本 role 或实现 F183 runner。

## TDD 与完成门

- Catalog RED 证明 create/add 目前拒绝 `fact/rationale`；
- MSQL/原生 Catalog round-trip 证明两种 role 经 daemon 使用的同一 Catalog 路径可读回；
- 既有未知 role、重复 title/summary、reopen 与全树测试保持全绿；
- format、vet、unit、race、integration、e2e 与 cross-build 全绿。

用户执行授权：2026-08-03 用户要求继续顺序完成后续 Feature；F183 clean-daemon RED 暴露
F182 fixture 的 `fact/rationale` 无法通过现行受控词表，故先以独立前置修复处理。

开工前结论：PASS。

## 完成证据

- Catalog create 覆盖大小写规范化，并无损保存 `fact/rationale`；
- 原生 Catalog 的 MSQL `CREATE TABLE ... ROLE` 与 `SHOW COLUMNS` round-trip 通过；
- 既有未知 role、重复 title/summary、全树 unit/race/integration/e2e 与双架构 cross-build 全绿；
- F183 不再需要丢弃或改写 F182 fixture 的 Column semantic role。

完成门结论：PASS。下一项恢复 F183 clean-daemon 物化与 answer runner。
