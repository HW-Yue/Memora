# F192：格式无关 Document IR v1

状态：已完成，2026-08-05。

## 唯一主要结果

冻结所有文档适配器共同产出的内存 Document IR：保留文档层级、确定性阅读顺序、资源摘要、
稳定 source anchor、表格层级和脚注/引用关系。IR 可以暂时承载解析出的正文供 Agent 阅读，
但它不是机械 chunk、不是 Memora Row，也不进入 Database、索引、snapshot 或长期导出。

F193 才实现 EPUB → IR；F194 才基于 IR 生成 ReadExtent/coverage；F196 才让模型读取 extent。

## 数据模型

```text
DocumentIR
├── SourceReference（F191 原始对象身份）
├── Resources[]（logical locator、media type、bytes、sha256）
├── Nodes[]（document/part/chapter/section/.../paragraph/table/row/cell/footnote）
│   └── Anchor(resource_id, byte_start, byte_end, selector?)
├── ReadingOrder[]（每个内容叶节点恰好一次）
└── References[]（footnote/citation/caption/internal link）
```

- Document ID 固定由 Source SHA-256 派生；Node ID 固定由
  `(document_id, parent_id, kind, sibling_ordinal)` SHA-256 派生；
- Resource ID 固定由 `(source_sha256, logical_locator)` SHA-256 派生；
- 每个 Node 必须有 anchor；范围不能越过对应 Resource bytes；
- root 唯一且为 `document`；parent 必须先于 child，sibling ordinal 从 1 连续；
- container 必须有 child，content leaf 不能有 child，并在 ReadingOrder 中恰好出现一次；
- `table → table_row → table_cell` 层级强约束；脚注引用 target 必须存在且为 footnote；
- 不提供自由 map、任意 kind 或 chunk 字段；节点/资源/正文总量有硬边界；
- `SealDocumentIR` 计算规范 SHA-256；`Validate` 重算并验证全部结构，strict decoder 拒绝未知字段；
- 所有构造、seal、decode 结果深拷贝，调用者不能改变已验证 IR。

## RED 与完成门

- RED 先证明 IR、Node/Resource/Anchor/Reference、stable ID、seal/decode API 不存在；
- 包含章节、段落、两列表格和脚注的 fixture seal→JSON→strict decode 无损；
- 同一输入产生相同 ID/digest，正文或结构变化改变 digest；JSON 不出现 chunk/embedding；
- parent/cycle/order、ordinal、leaf/container、table、reading order、anchor 和 reference 负例全覆盖；
- 超节点/资源/正文预算拒绝，所有公开结果深拷贝；
- 目标测试、`-race`、Agent import allowlist 与完整 CI 全绿。

用户执行授权：2026-08-05 用户要求持续执行至 F204。

## 完成证据

- Document/Resource/Node ID 均有版本化稳定摘要函数，同输入重复 Seal 得到逐字相同 IR SHA-256；
- fixture 保留 chapter、heading、paragraph、table/row/two cells、footnote 与引用边，strict JSON round-trip 无损；
- parent-before-child、连续 ordinal、container/leaf、table 专属层级和 reading-order 全叶覆盖由 Validate 强制；
- 每个 Node anchor 必须命中已知 Resource 且范围不越界，footnote/caption target 类型受控；
- 单节点 1 MiB、总正文 64 MiB、Node/Resource/Reference 数量与 JSON 大小均有硬上限；
- SHA-256 篡改和 JSON 未知字段 fail closed，Seal/Clone/Decode 均不暴露可变 slice；
- 编码字段不包含 chunk、embedding 或 vector，生产实现只使用标准库且不接触 Database/MSQL。

完成门结论：PASS。下一项为 F193 EPUB 确定性适配器。
