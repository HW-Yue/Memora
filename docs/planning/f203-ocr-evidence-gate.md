# F203：OCR/视觉路径证据门

状态：已完成；2026-08-05 规格冻结并通过完成门。

## 唯一主要结果

提供一个不包含 OCR 引擎、模型权重或浏览器运行时的确定性证据门。它接收外部评测器产出的
逐页、成对 baseline/OCR 结果，校验来源和样本边界，计算可复核的收益与延迟，并只返回
`eligible` 或 `deferred`。证据不足时必须延后，不允许因为“扫描页存在”就把 OCR 变成默认路径。

## 规则

- 每页最多一条样本，页号从 1 开始；样本必须属于同一个 `sha256:` corpus；
- 同时记录是否有文本层、baseline/OCR 是否答对以及两者延迟；不接收原文、模型输出或 API key；
- 默认门槛：至少 32 个扫描页、32 个成对样本、OCR Recall 提升 5 个百分点，且平均额外延迟不超过 500ms；
- 所有指标使用整数 basis points 和整数毫秒，报告可重复生成并带 digest；
- 不满足任一门槛返回 `deferred` 与稳定原因码，不能伪造“质量已通过”；满足全部门槛才返回 `eligible`。

## 完成门

先让 RED 测试在缺少 gate 时失败，再验证 determinism、重复页/错 corpus/越界输入、门槛通过和
证据不足延后。该 Feature 不调用真实模型，也不进行批量评测；真实 OCR 运行时和权重仍作为后续
 可选组件单独 Review。

## 完成证据

- `internal/agent/ocr_evidence.go` 仅接收逐页摘要，不依赖 OCR SDK、模型权重或网络；
- 通过门槛、证据不足延后、重复页、零页号和非法 corpus 测试；报告含稳定 digest，重复计算结果一致；
- `go test ./internal/agent -run TestOCREvidenceGate -count=1`、race 与 vet 通过；本次没有真实模型调用或批量质量评测。

## 关联

- [文本层 PDF 适配器](./f202-text-pdf-adapter.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
- [当前产品基线](../product/current-product.md)
