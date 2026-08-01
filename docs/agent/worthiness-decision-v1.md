# Worthiness Decision v1

状态：F134 已完成（2026-08-01）。

## 定位

Worthiness Decision 是 AI 对一条 pending Host Input 的终结判断。它只允许 IGNORE、WRITE、
REVISE；引擎验证 capture binding、授权和 Mutation Receipt，AI 对语义正确性负责。

标准顺序：

```text
capture → discover/query/preflight → mutate → decide → stable receipt
```

Decision API 不执行 MSQL。WRITE 对应已提交且已验证的 INSERT receipt，REVISE 对应同条件
的 REVISE receipt；IGNORE 对应经过 preflight 的 ignored receipt。`committed_unverified`
不能终结候选。

## 协议与形状

`memora.worthiness-decision/v1` 绑定 decision/input ID、workspace、actor、capture input/scope
hash、完全相同的授权 Database 集合、verdict、短 reason 和完整 Mutation Receipt。

- IGNORE 不带 target；receipt 必须是 verified IGNORE/ignored，且没有 change；
- WRITE/REVISE 带 Database/Table/RowID/revision target；目标 Database 必须授权，且目标
  RowID/revision 必须恰好匹配一个 INSERT/UPDATE change；
- 决策 actor 必须是 capture actor，防止无身份交接静默终结候选。

成功返回 `memora.worthiness-receipt/v1`、`status=finalized`、verdict、decision/input/scope
hash、Mutation Plan ID 和 engine decision time，不包含 candidate text 或 reason。

## 原子性与恢复

同一事务保存不含 candidate text 的 decision record、建立 decision ID 索引并删除 pending
正文。失败则三者都不变；成功后 inbox 容量立即释放。

同一 canonical decision 重试返回原时间/hash 并标记 replay；同 input 异 decision 或同
decision ID 指向另一 input 都返回 revision conflict。重启后可用：

```sh
memora decide --receipt decision-12 --workspace project-memora
```

恢复结果包含 decision 和 receipt，但不恢复已终结正文。正式事实仍只存在于先前成功的
MSQL mutation；IGNORE 不产生事实。

## 关联

- [Host Input Capture v1](./host-input-capture-v1.md)
- [Skill 写入流程 v1](./skill-write-v1.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
