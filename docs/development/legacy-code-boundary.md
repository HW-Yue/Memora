# 旧代码清理边界

状态：2026-08-02 当前审计；只删除已退出产品路径且没有运行时、迁移或对拍职责的实现。

## 已确认退出

- F42 `internal/benchmark` 与 `memora.ai-benchmark/v1`：评分器和脚本 Adapter 不能代表
  当前 Table Route + SQL 事实读取，也没有生产命令引用；F164 已从活动代码和 benchmark
  目录移除，冻结 corpus 只留在归档；
- F43 `internal/runtimegate`：只在自身测试和历史 Phase C 测试中计算 defer，当前权威由
  ADR、Feature 状态与新评测架构表达；F164 已删除；
- F30 `internal/skillquery`：硬编码一个未接入产品的 Agent 查询循环，与 Canonical Skill
  和后续内置评测 Agent 重复；F165 已删除；
- 历史 `MATCH`：主语法和执行器已在 F71 删除，但 Lexer/Policy/测试工具仍有残留；列为
  独立清理项。

## 明确保留

- `compat/sqlite-migrator/`：只读旧 SQLite、生成逻辑快照并显式迁移；daemon 不 import；
- `internal/nativemigration`：检测旧实例并拒绝静默 fallback；
- `internal/catalog`、`internal/row`、`internal/router`、`internal/snapshot`：仍被迁移、
  package、逻辑快照和 native parity/reference-model 测试使用；不是生产存储 authority，
  但目前不能删除；
- Page index legacy reader 与旧 snapshot decoder：承担已发布格式的显式升级和 corruption
  证据；
- MCP 旧协议版本：属于明确的客户端兼容面，不是旧产品检索思路；
- `routelexical`、`routevector`、`routeexact`：是 ADR-0007 允许的 Route-only 导航预测器，
  不是已撤销的 Row/chunk 检索。

## 删除规则

删除前必须证明目标不在 `cmd/...` 生产依赖图中，并先增加“旧路径不得重新出现”的 RED。
迁移 reader、格式 decoder、reference model 或兼容协议不能仅因名称含 `legacy` 删除；只有
支持窗口和替代证据另行冻结后才能清理。

## 关联

- [当前产品基线](../product/current-product.md)
- [Feature 状态](../planning/feature-status.md)
- [后续路线](../planning/future-roadmap.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
