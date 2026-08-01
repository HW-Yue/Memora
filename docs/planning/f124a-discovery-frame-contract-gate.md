# F124a Discovery Frame Contract 开工与完成门

状态：已完成。

## 单一主要结果

冻结并实现一个可挂入 Result Envelope 的版本化 Discovery Frame，使多个可回退
predictor 在同一 snapshot 和全局预算内返回带来源的导航候选。

## 产品门

- 用户故事：Agent 一次拿到可审计候选，预测失败时继续普通 Router，而不是重做整次查询。
- AI-native：候选帮助 AI 少调用一次模型，但选择权仍在 Agent，事实仍由 Row SQL 提供。
- 失败隔离：单个 predictor unavailable 不升级为 statement failure。
- 权限边界：Frame 只组合调用方已获授权的位置，不新增物理或越权读取通道。

结论：PASS。

## RED 证据计划

入口：`go test ./internal/discovery ./internal/result`

1. `TestBuilderRejectsMixedSnapshotAndCatalogRevision`：批次混用读取视图必须稳定失败。
2. `TestBuilderEnforcesOneGlobalCandidateAndByteBudget`：两个 predictor 不能分别耗尽整份 LIMIT。
3. `TestUnavailablePredictorIsReceiptNotQueryFailure`：不可用预测器形成零候选成功 Frame。
4. `TestFrameRejectsInvalidProvenanceLocationsAndScores`：缺来源、Route 跳级、NaN 与重复位置拒绝。
5. `TestResultEnvelopeCarriesDiscoveryAndPropagatesTruncation`：Frame 只能挂在成功结果且截断向上传播。
6. `TestDiscoveryFrameWireRoundTripAndUnknownFieldCompatibility`：稳定 JSON、未知字段兼容、未知 enum 拒绝。

RED 已确认：首次运行因 `internal/discovery` 无生产代码、Result Envelope 无 Discovery
字段而构建失败；实现后同一组测试转绿。

## 明确不做

- 不增加具体 MSQL statement 或 parser/executor；
- 不实现 lexical/vector 算法、embedding、HNSW 或预取策略；
- 不建立持久化索引或新的 Catalog MVCC；
- 不允许候选携带 Row、snippet 或答案。

## 完成门

- 上述 RED 全部转绿，`go test -race ./internal/discovery ./internal/result` 通过；
- `./scripts/ci.sh`、全量 race、格式和生成检查通过；
- 协议文档与 Result Envelope/后续 Feature 状态同步；
- Review 确认跨 snapshot、全局预算、provenance、unavailable 和导航-only 边界。

## 完成证据

- `go test -race ./internal/discovery ./internal/result`：通过；
- `./scripts/ci.sh`：format、vet、unit、全量 race、integration、e2e、cross-build 全通过；
- 混合 snapshot/catalog revision、全局候选/字节预算、不可用 predictor、非法位置、
  NaN/未知 enum、重复来源和 Result Envelope 截断传播均有测试；
- `navigation_only` Frame 的类型不含 RowID、正文、snippet、答案或原始 query；
- 未覆盖项仍是 F124b–F124e 的预测算法、MSQL 能力和持久化 generation。
