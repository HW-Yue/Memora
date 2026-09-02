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
- F25 `internal/generation`：只服务于 Router/Agent/mechanical 三类旧组合 manifest，
  不在生产依赖图中；F167 已删除，旧规格移入归档；
- 历史 `MATCH`：主语法和执行器已在 F71 删除；F166 已清除 Lexer、Policy、测试工具
  和当前文档中的可用语句残留，并用 Parser/CI 回归约束锁定。

## 明确保留

- `compat/sqlite-migrator/`：只读旧 SQLite、生成逻辑快照并显式迁移；daemon 不 import；
- `internal/nativemigration`：检测旧实例并拒绝静默 fallback；
- `internal/catalog`、`internal/row`、`internal/router`、`internal/snapshot`：
  **2026-09-02 订正**——原来的保留理由里，「package」这一条已经没有了
  （`internal/dbpackage`／`internal/wikiexport` 整包删除），
  native parity 对拍测试也随之删除。**现在唯一的保留理由是测试夹具**：
  39 个测试文件、8201 行拿 `catalog.New`／`row.New` 建被测对象。
  所以这几个包的删除条件已经收敛成一件事——**把那批测试迁到 native 栈**，
  而不是等某个功能先解耦。注意 `model.go` 里的类型是活的，删除只针对 Service 层。
  排期见[执行计划](../planning/execution-plan.md)清理台账；
  - 补充（2026-08-22）：`internal/router/service.go` 的 **membership 部分**
    （`ReplaceMembershipsIn`、`MembershipsForRowIn`、`router_leaf_members` 与
    `router_row_memberships` 两个 bucket）对 daemon **已是死代码**——生产只构造
    `nativerouter.New`（`internal/daemon/lifecycle.go:199`），而 `row.New(` 的调用方
    全是测试。它随[叶子直挂 RowID](../storage/leaf-rowid-v1.md) 的迁移一并退场，
    但删除仍受本文下面的删除规则约束：先证明不在生产依赖图中，并先加 RED。
    同一迁移里 `nativerouter.Repository.Attach`（`repository.go:134`）也没有
    非测试调用方；
- Page index legacy reader 与旧 snapshot decoder：承担已发布格式的显式升级和 corruption
  证据；
- MCP 旧协议版本：属于明确的客户端兼容面，不是旧产品检索思路；
- `routelexical`：是 ADR-0007 允许的 Route-only 导航预测器，
  不是已撤销的 Row/chunk 检索。
  - **`routevector`／`routeexact` 已于 2026-09-02 删除**（`3ff6136`）。
    删的理由不是 ADR-0007 改了，是 `routevector.Generation.vectors`
    把全部 Route 向量常驻内存、无上界无淘汰，为一个到不了用户手里的功能服务。
    重做时方向不变，但必须做成盘上索引。

## 删除规则

删除前必须证明目标不在 `cmd/...` 生产依赖图中，并先增加“旧路径不得重新出现”的 RED。
迁移 reader、格式 decoder、reference model 或兼容协议不能仅因名称含 `legacy` 删除；只有
支持窗口和替代证据另行冻结后才能清理。

## 关联

- [当前产品基线](../product/current-product.md)
- [Feature 状态](../planning/feature-status.md)
- [后续路线](../planning/future-roadmap.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
