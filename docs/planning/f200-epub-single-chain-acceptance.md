# F200：冻结 EPUB 单条干净全链路验收

状态：实现中；2026-08-05 规格已冻结。

## 唯一主要结果

在全新 Memora Instance 中，用一份仓库内确定性构造、内容冻结的小 EPUB 跑通 SourceStore → EPUB
Document IR → coverage → draft ledger → independent review → approved MSQL submit → Source Receipt →
固定 Query Agent，并输出一个不含原文的结构化验收报告。

本项只回答“链路能否跑通、结果结构是否一致”。不并发请求真实模型，不运行 36 题/多 arm/多模型
批量评测，不计算 Recall/MRR/Ragas，也不据此声明答案质量达标。

## Runner 边界

`internal/assimilationacceptance.Runner` 只编排已经完成的 F191–F199 和 F181 组件：

- 注入 SourceStore、EPUB adapter、Drafter、Ledger、Reviewer、Reconciler、Query Agent、Clock；
- 用户审批仍由注入的 `PlanApprovalSource` 提供，Runner 不伪造或静默批准 plan；
- 用户 Database 的所有发现、提交和查询继续经各组件的 `MSQLExecutor`；Runner 不导入
  Catalog/Row/Router/Page/Store 内核，也不增加旁路写 API；
- 失败时尽力释放临时 source；成功时必须确认 SourceStore reference/object 已清理。

## 冻结样本与确定性 Provider

CI fixture 是一个 EPUB 3 ZIP，包含 mimetype、container、OPF、nav 和单章 XHTML。章节只陈述一个
可验证事实，author Provider 按真实 extent node ID 生成一条参数化 INSERT；review Provider 从请求
中的 challenge/node/numeric inventory 生成完整 accepted check；固定 Query Provider 先发一条
SELECT，再从真实 SELECT result 返回答案。

三类 Provider 都是测试内确定性 adapter，不模拟模型质量。它们仍经过正式 Provider Request/
Response 和 required-tool 校验，因此能证明上下文、工具协议和调用顺序可执行。

## 验收报告

`memora.assimilation-chain-acceptance/v1` 只包含：run/source/document digest、EPUB parse receipt、最终
coverage、extent/claim/review 数量、plan ID/digest、F199 Source Receipt、固定问题与答案、SELECT
evidence 行数、draft/review/query Provider 调用和 token 总数、各阶段耗时、Source cleanup 计数。

报告不包含 EPUB bytes、Document IR 正文、ReadExtent、候选 MSQL/参数、prompt、tool arguments、
Provider raw response 或 reviewer 推理。

## 完成门

RED 先锁定：真实 clean daemon、冻结 EPUB digest、完整阶段顺序、一个 accepted claim、hash-bound
approval、正式 SUBMIT+SHOW、收据 RowID 与 SELECT evidence RowID 一致、固定最终答案、调用/usage/
耗时结构、source cleanup、报告无正文，以及失败 cleanup、race、vet、import boundary 和全量 CI。

## 关联

- [F193 EPUB 适配器](./f193-epub-adapter.md)
- [F199 Reconciliation 与 Source Receipt](./f199-assimilation-reconciliation.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
