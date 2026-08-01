# F110 Metadata Read Protocol 开工与完成门

状态：已完成，完成门 PASS。

## 唯一主要结果

Admin 可通过有界 MSQL、而不是 Store API，分页读取 Database、Table 和 Column 元数据。

## RED

1. `SHOW DATABASES/TABLES/COLUMNS` 没有 limit/cursor/snapshot/version；
2. Catalog 大于调用方上下文预算时只能完整列举；
3. 两页之间发生 DDL 时，调用方会静默拼接不同 Schema 状态；
4. cursor 损坏、跨列表复用和授权过滤后的分页没有协议证据。

最小 RED（2026-08-01）：新增 parser/result/executor 契约测试后，当前代码因不存在
`result.ListPage`、Catalog cursor 语法和 `Output.Page` 而编译或断言失败。

## GREEN

- Catalog `SHOW` 支持 cursor/limit parameter，缺省 64、硬上限 256；
- statement result 返回 `memora.list-page/v1`；
- snapshot 绑定可见列表，DDL 后 continuation fail-fast；
- cursor 完整性、scope、offset 和授权后分页均有测试。

## 不做

- Catalog B+ Tree 原生 range cursor 优化；
- Route、Row、Change、Trace read protocol；
- Catalog 历史 MVCC read view 或 Admin HTTP API。

## 完成门

- [x] parser/parameter/limit/cursor 边界；
- [x] result envelope golden、校验与兼容；
- [x] 多页、snapshot conflict、tamper/scope mismatch、授权过滤；
- [x] targeted/full/race/integration/e2e/cross-build；
- [x] 独立 commit；合入动作在本文件随 commit 快进到 `main` 时完成。

完成证据：Database/Table/Column 三类分页、64 默认值、256 硬上限、参数绑定、
snapshot continuation、DDL conflict、cursor strict decode/tamper/scope、授权后分页、result
golden 和 native Catalog reopen 均有自动化测试；`scripts/ci.sh` 全绿。完成后结论：PASS。
