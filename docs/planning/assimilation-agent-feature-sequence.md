# 资料吸收 Agent Feature 序列

状态：2026-08-03 候选序列；先验证小网页写入，再进入整本书，不构成整批实现授权。

## 两条垂直链

短网页不强行进入大文档任务系统：

```text
当前网页/短文本 → Agent 阅读 → 语义模块草稿 → MSQL 短事务 → SELECT 回读验证
```

整本书使用可恢复、可交互的长任务：

```text
上传/路径 → inventory → 询问是否全部吸收及目标 scope
→ Agent-owned SourceStore + Document IR
→ ReadExtent/coverage 调度 → draft/claim ledger
→ 发现问题立即发事件并暂停受影响分支
→ 独立复核 → MSQL 短事务提交 → reconciliation → Source Receipt → 清理暂存
```

完整 PDF、EPUB、图片、机械 chunk 和模型阅读窗口都不进入 Memora Database。Agent 可持有
临时任务数据；正式语义模块、关系、状态和收据只能通过版本化 MSQL 进入数据库。

## 依赖图

```text
F181 只读 Query Agent + F185 质量门
→ F187 写能力/审批 → F188 单网页写入垂直链
→ F189 交互协议 → F190 Job → F191 SourceStore → F192 Document IR → F193 EPUB
→ F194 coverage → F195 正式 MSQL 吸收面 → F196 draft ledger
→ F197 暂停恢复 → F198 独立复核 → F199 reconciliation/receipt → F200 EPUB benchmark
→ F201–F205 证据触发扩展
```

## A：先证明小写入（F187–F188）

| Feature | 唯一主要结果 | 完成证据 |
| --- | --- | --- |
| F187 | Policy 强制的 Agent write profile 与审批信封 | prompt 失控也不能越权；revision/schema/affected-row guard |
| F188 | 当前单网页/短文本直接写入与回读 | 不建长任务；MSQL draft→approve→commit→SELECT verify；失败无半提交 |

F188 是写入 Prompt、Schema 选择、重复事实检测和回读验证的最小真实实验。它不过门时，
不以更多文档格式掩盖写入质量问题。

## B：长任务基础（F189–F195）

| Feature | 唯一主要结果 | 完成证据 |
| --- | --- | --- |
| F189 | Source intake 交互与事件协议 | 上传后先确认范围；问题、进度、等待用户和错误即时流式输出 |
| F190 | 可持久恢复的 AssimilationJob 状态机 | command/event/checkpoint 幂等、崩溃恢复、取消与并发测试 |
| F191 | 内容寻址临时 SourceStore | 不进 Database；权限、配额、hash、清理和 reopen |
| F192 | 与格式无关的 Document IR v1 | 层级、阅读顺序、anchor、表格/脚注引用稳定，无机械 chunk 语义 |
| F193 | EPUB 确定性适配器 | spine、目录、章节、脚注、资源清单和 malformed corpus |
| F194 | `ReadExtent` 与 coverage 调度 | 全部必读范围覆盖；窗口 hash；断点续读；不持久化原文 |
| F195 | 正式 MSQL 吸收提交 surface | review/submit/receipt 经 Parser、Policy、事务；取代 Agent 所需私有 IPC |

F189–F194 的操作状态归 Agent 所有，因此可以在 F195 前实现；一旦状态或动作要进入用户
Database，就必须等待 F195，不能临时导入旧 Assimilation Controller。

## C：AI 写入与复核（F196–F200）

| Feature | 唯一主要结果 | 完成证据 |
| --- | --- | --- |
| F196 | DeepSeek/Kimi 可替换的 draft/claim ledger | 每个 claim 绑定 source anchor、模型、prompt、输入 digest 与候选 MSQL |
| F197 | 问题驱动的暂停与恢复 | 只暂停受影响分支；用户回答形成版本化 command；不丢 coverage |
| F198 | 独立 review gate | reviewer 与 author 隔离；数字、anchor、冲突、未保存原文四项验证 |
| F199 | 短事务 reconciliation 与 Source Receipt | in-doubt 不盲重放；实际 RowID/revision/commit sequence 可追溯 |
| F200 | 整本 EPUB 冻结 benchmark | 干净 snapshot 吸收后由固定 Query Agent 回答隐藏问题并输出成本/耗时 |

写入模型和查询模型分别固定。隐藏答案不提供给写入模型；不能让 author/reviewer 自评最终
正确性。F200 不通过时，先定位 coverage、draft、review 或 query 层，不增加 OCR。

## D：只按证据扩展（F201–F205）

| Feature | 进入条件 |
| --- | --- |
| F201 DOCX adapter | EPUB 链通过，真实 DOCX 结构样本已冻结 |
| F202 text-layer PDF adapter | EPUB 链通过，阅读顺序和表格样本已冻结 |
| F203 OCR/视觉路径 evidence gate | 不可读页比例和答案收益证明值得增加可选模型资源 |
| F204 外置 Agent Hook | 只采集 Memora 调用、session/host/model 与有界结果，不采宿主完整上下文 |
| F205 Admin 私有诊断 | Trace schema 稳定后按 run/session/model 展示质量、耗时、调用与成本 |

OCR、本地模型权重和浏览器运行时默认不进入主安装包。外部评测框架与 Python 依赖也只属于
开发工具链。

## 关联

- [查询 Agent Feature 序列](./query-agent-feature-sequence.md)
- [资料清单与覆盖](../agent/assimilation-coverage-v1.md)
- [资料独立复核与提交](../agent/assimilation-review-v1.md)
- [可选内置 Agent Runtime](../agent/embedded-agent-runtime.md)
