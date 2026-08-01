# Host Input Capture v1

状态：F133 已完成（2026-08-01）。

## 定位

Host Input inbox 是 auxiliary staging state，用于在 AI 做 worthiness 决策前稳定接住一条
短候选。它不是 Database/Table/Row、History、Source Receipt 或 Router，也不参与查询答案。

```sh
memora capture --candidate '<memora.host-input/v1 JSON>'
memora capture --receipt input-12 --workspace project-memora
```

## Input 约束

`memora.host-input/v1` 必须包含 input ID、workspace、actor、1–32 个精确授权 Database
selector、一个 source 和一个 `candidate_text`。candidate text 是单个 UTF-8 候选，去除
首尾空白后最大 12,000 bytes；不接受二进制、chunk 数组、Embedding 或多文档清单。

来源只允许：

- `conversation_assertion`：不能携带 locator 或 source hash；
- `document_anchor` / `repository_anchor`：必须同时携带短 locator 与 source SHA-256。

Capture 不能自封 `reviewed_source`。完整文档、目录、媒体和多窗口资料继续走
[资料清单与覆盖 v1](./assimilation-coverage-v1.md)。

## Receipt、幂等与恢复

成功返回 `memora.host-input-receipt/v1`、`status=pending`、canonical input/content/scope
hash、正文 byte 数、授权范围和 engine capture time。Receipt 不回显 candidate text。

同 input ID + canonical 内容重试返回原 capture time/hash 并标记 `replayed=true`；同 ID
异内容拒绝为 revision conflict。daemon 重启后，宿主可用 input ID + workspace 显式重载
pending candidate；workspace 不匹配不能读取。全局 pending inbox 最多 256 条。

## 与 F134 的边界

`pending` 只证明候选被稳定接收，不表示内容正确、重要、非重复或应写入。F133 不做
Database discovery、查重、Schema/Route 选择和 MSQL mutation。F134 才能把同一 receipt
推进到 ignore/write/revise 决策，并承担终结 pending 生命周期的责任；该闭环已由
[Worthiness Decision v1](./worthiness-decision-v1.md)实现。

## 关联

- [AI-native 产品宪章](../product/ai-native-product-charter.md)
- [Canonical Skill v1](./canonical-skill-v1.md)
- [资料吸收](../data/assimilation.md)
