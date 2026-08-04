# F201：DOCX 确定性适配器

状态：实现中；2026-08-05 规格已冻结。

## 唯一主要结果

实现 `SourceStore DOCX → Document IR v1` 的确定性适配器。适配器读取 OOXML package relationship、
content type、正文、样式、脚注和 document relationship，保留段落/标题/列表/表格/脚注及内部资源
关系；不调用模型、不生成 chunk、不写 MSQL/Database，也不引入 Office、LibreOffice 或第三方 SDK。

## 固定解析链

```text
SourceStore.OpenRandomAccess(job, source)
→ ZIP 路径/加密/entry budget
→ [Content_Types].xml + _rels/.rels
→ officeDocument target → word/document.xml
→ styles.xml / footnotes.xml / document.xml.rels
→ paragraph/heading/list/table/row/cell/footnote/image
→ footnote 与内部 image relationship
→ SealDocumentIR
```

- MIME 固定为 OpenXML Word document；ZIP entry 拒绝 absolute、`..`、反斜杠、重复、加密和超预算；
- package root relationship 必须唯一指向受包内约束的 officeDocument part，不能按文件名猜正文；
- content type override/default、relationship ID/target/mode 必须唯一，所有引用 target 必须存在；
- 正文顺序完全按 `w:body`；run text、tab、break 规范化但不按字符数切片；
- paragraph style/outline 映射 heading，`numPr` 映射 list/list_item；表格严格映射
  `table → table_row → table_cell`；
- footnote reference 必须命中 `footnotes.xml` 中的非 separator footnote；image relationship 只解析包内
  resource，不访问外部 URL；v1 拒绝 `altChunk`、宏、OLE、active content 和加密包；
- 每个 ZIP file 都进入资源摘要清单；Node anchor 使用对应 XML part 的稳定 byte span；
- entry、单/总解压字节、XML token/depth、节点和正文继续受硬预算约束；同一输入必须产生相同 ID/digest。

## 完成门

RED 先冻结一个手工构造、ZIP 顺序与正文顺序不同的 DOCX：标题、普通段落、两项列表、两列表格、
脚注引用和内部图片 relationship 均进入有效 IR；重复解析逐字一致。负例覆盖 root relationship、
content type、traversal、duplicate、encrypted、dangling relationship/footnote、malformed XML、空表格单元、
entry/XML budget、context cancellation、Source corruption 和 close failure。

目标 package、`-race`、vet、Agent import boundary 与完整 CI 全绿。F201 不运行真实模型或答案评测，
也不改变 F200 暴露的 native multi-statement assimilation 事务缺口。

用户执行授权：2026-08-05 用户要求持续执行至 F204。

## 关联

- [Document IR v1](./f192-document-ir-v1.md)
- [EPUB 确定性适配器](./f193-epub-adapter.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
