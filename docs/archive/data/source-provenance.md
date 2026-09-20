# 来源强度与复核证明

状态：F78 已实现。

## 四种来源强度

每条 Row History revision 明确记录一种 `source_kind`：

- `conversation_assertion`：用户或 Agent 在对话中陈述；这是普通写入的默认值；
- `document_anchor`：可定位文档范围，必须带 locator 与内容 SHA-256；
- `repository_anchor`：可定位仓库对象，必须带 locator 与内容 SHA-256；
- `reviewed_source`：完整覆盖和挑战式复核通过，另绑定 Source Receipt ID。

没有 Source Receipt 的普通 INSERT 绝不显示为 reviewed。`SHOW HISTORY` 返回
source kind、receipt ID、locator 和 content hash，事实正文仍通过 `SELECT` 读取。

## Challenge-bound review

coverage 完成后，引擎生成绑定 task、source hash、coverage revision 和 finish event
的 `review_challenge`。提交必须满足：

- reviewer 身份不同于 draft author，review context 也不同于 draft context；
- challenge 与引擎保存值一致；
- findings digest 绑定复核报告，但长期 Receipt 不保存复核推理；
- artifact digest 绑定 challenge、草稿、coverage、精确模块/关系/关键事实集合及
  四项检查结果；
- 任一字段被改写都会在任何 Row Tool 调用前失败。

Context ID 本身不再被当作隔离证明。引擎验证可审计 artifact；实际启动独立
reviewer context 仍由宿主负责。没有可信宿主签名时，Memora 不声称能读取或
密码学证明模型的隐藏上下文。

## 资料提交

Inventory 只接受 `document_anchor` 或 `repository_anchor`。审核通过后，
Assimilation Controller 在执行 Mutation Plan 前注入：

```text
source_kind = reviewed_source
source_receipt_id = submission_id
source_locator = inventory.source.locator
source_content_hash = inventory.source.content_hash
```

这些字段随原生 History 持久化、快照导入导出，并与 Source Receipt 中的 impact
互相定位。原始窗口、机械 chunk 和 reviewer 推理不进入 Row 或 Receipt。

