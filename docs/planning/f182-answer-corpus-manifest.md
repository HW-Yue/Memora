# F182：Answer Corpus / Manifest v1

状态：已完成；单项 Review、RED → GREEN → REFACTOR 与全部完成门均已通过。

## 唯一主要结果

冻结首个可重放的 Query Agent answer corpus：确定性生成器、公开 manifest、evaluator-only ground
truth、严格 loader/validator 和 blind task 投影。F182 不运行 Agent、不调用模型、不物化数据库、
不评分答案，也不实现 F183 runner 或 F184 外部评分 adapter。

## Fixture snapshot

当前 Logical Snapshot 不完整表达 Route 权威，F182 不伪称它能承载本题库。manifest 内冻结一个
`memora.answer-fixture/v1` 逻辑 fixture：Database/Table/Column、Route tree、单 Row Leaf、Row values
和稳定 ID；F183 必须只经 MSQL 将它物化到干净实例，并以 fixture `sha256` 作为 benchmark snapshot
identity。fixture 按稳定 ID 排序，digest 排除自身 digest 字段后对规范 JSON 计算。

第一版使用项目自有合成数据，覆盖多 Database/Table、中文/英文/混合问法、直接事实、改写、
多 Row 和无答案场景。每个 Row 绑定 source ID；source 声明 synthetic、生成器 locator、fixture
content digest 与许可证。许可证固定仓库 `PolyForm-Noncommercial-1.0.0`、`LICENSE` 路径和完整
Required Notice，不为 corpus 擅自改成其他许可证。

## Manifest 与隐藏答案

- public manifest 版本为 `memora.answer-corpus-manifest/v1`，包含 corpus/revision、license/source、
  fixture、问题、授权 Database scope、语言/类别、冻结预算、ground-truth digest 和 manifest hash；
- ground truth 版本为 `memora.answer-ground-truth/v1`，单独保存 reference answer 与所需事实坐标
  `(database/table/row/column/value)`，自己的 hash 必须等于 manifest 声明；
- 两文件开源可审计；“hidden”指 F183 传给 Agent 的 `BlindTask` 绝不含 reference、fact、正确
  RowID/Route 或 ground-truth path，而不是对仓库读者保密；
- ground truth case 集必须与 manifest 一一对应；answerable facts 必须实际引用 fixture 值，
  unanswerable 必须无 facts；manifest、fixture、truth 任一篡改或未知/trailing 字段均拒绝。

## API 与完成证据

`internal/answercorpus` 只依赖标准库，提供 `Generate`、严格 `Load`、规范 `Encode*`、`Validate`
和 `BlindTask(caseID)`。checked-in 文件位于 `benchmarks/answer-retrieval-v1/`，必须逐字等于生成器。

- golden 验证重复生成 byte-for-byte 一致、所有 digest/hash 自洽；
- matrix 验证 language/category/Database/Table、多事实与负例覆盖，Leaf 固定 0..1 Row；
- import-ready fixture 验证 ID、父子连接、Row/Column/source 引用和排序；
- blind task 反射与 JSON 泄漏扫描证明无 reference/fact/ground-truth 字段或内容；
- unknown/trailing、重复/乱序、hash 篡改、许可/source 缺失、悬空 fact 全部拒绝；
- unit/race/vet、完整 CI 与 cross-build 全绿。

用户执行授权：2026-08-03 用户要求持续顺序完成全部已讨论 Feature。本 Review 只批准上述 F182 范围。

开工前结论：PASS。

## 完成结论

- `internal/answercorpus` 已提供确定性 `Generate`、严格 `Load`/`Validate`、规范编码和
  model-blind `BlindTask`；
- `benchmarks/answer-retrieval-v1/` 已冻结 3 个 Database、12 个 Row、12 个中英混合问题，
  覆盖 direct、paraphrase、multi-fact 和 unanswerable；
- generator 与 checked-in manifest/ground truth 逐字一致，fixture、truth 与 manifest 三层
  SHA-256 绑定，未知字段、trailing JSON、乱序、篡改、悬空事实和答案泄漏均 fail closed；
- format、vet、unit、race、integration、e2e 与 cross-build 全绿。

完成门结论：PASS。下一项是 F183 answer runner；真实模型运行仍依赖 F180 有效鉴权。
