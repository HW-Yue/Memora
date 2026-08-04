# F194：ReadExtent 与 coverage 调度

状态：实现中；2026-08-05 规格已冻结。

## 唯一主要结果

把已验证的 Document IR 按阅读顺序调度为有界、可恢复的 `ReadExtent`，并用摘要确认每个
必读语义叶节点恰好被覆盖。它只证明 Agent 完整读过资料，不写知识，也不替代 F198 复核。

## 冻结契约

- F192 `ReadingOrder` 中的全部节点都是必读范围；位置使用半开区间 `[start,end)`。
- 一个 extent 由一个或多个完整语义叶节点组成。预算不足以容纳下一个完整节点时停止；
  单节点本身超限则稳定失败，禁止按字符或 token 机械切开。
- extent 携带节点正文、anchor 与不含正文的祖先结构上下文，仅存在于当前模型调用输入。
- 同一时刻最多有一个未确认 extent；重复 `Next` 必须返回字节语义相同的窗口。
- `Acknowledge` 同时校验 sequence 与规范 SHA-256；过期、乱序或不同摘要 fail closed，
  同一成功确认可幂等重试。
- checkpoint 绑定 Document IR 摘要，只保存 revision、已覆盖位置、待确认范围/摘要与最后
  确认收据；不得出现节点正文、label、content、body、text 或 chunk。
- 从 checkpoint 恢复时必须用原 Document IR 重建待确认窗口并复核摘要；IR 变化、截断、
  未知字段、非法范围或 checksum 损坏均拒绝恢复。
- `Complete` 仅在全部阅读顺序节点已确认且没有待确认窗口时成立。
- scheduler 必须支持并发调用且可通过 `-race`；返回值不得与内部状态共享可变 slice。

## RED 与完成证据

RED 命令：

```text
go test ./internal/agent -run TestReadCoverage
```

失败应证明尚无确定性窗口、确认、恢复和覆盖完成能力，而不是坏 fixture。完成门包含：顺序
全覆盖、预算边界、幂等/乱序确认、checkpoint 无原文、corruption/document mismatch、并发
确认、package race、全量 CI。

## 明确不覆盖

- F195 MSQL 吸收提交面；
- F196 模型 draft/claim ledger 与 prompt；
- F197 用户问题分支；
- 模型 token 估算、正文压缩或 OCR。

## 关联

- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
- [Document IR v1](./f192-document-ir-v1.md)
- [资料清单与覆盖 v1](../agent/assimilation-coverage-v1.md)
