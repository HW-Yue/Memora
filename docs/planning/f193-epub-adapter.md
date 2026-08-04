# F193：EPUB 确定性适配器

状态：已批准，2026-08-05 开工。

## 唯一主要结果

实现 `SourceStore EPUB → Document IR v1` 的确定性适配器。适配器严格读取 EPUB container、OPF
manifest/spine、EPUB3 nav 或 EPUB2 NCX，按 spine 组织章节，而不是按 ZIP entry 或文件名猜顺序；
XHTML 结构映射为 F192 Node/anchor/reference，最后 Seal 为可验证 IR。

F193 不调用模型、不生成 chunk、不写 MSQL/Database、不做 coverage，也不处理 DOCX/PDF/OCR。

## 固定解析链

```text
SourceStore.OpenRandomAccess(job, source)
→ ZIP/mimetype/entry budget
→ META-INF/container.xml → OPF
→ metadata + manifest + spine + nav/NCX
→ bounded strict XHTML DOM
→ chapter/heading/paragraph/list/table/row/cell/footnote/image
→ internal/footnote references + source byte anchors
→ SealDocumentIR
```

- ZIP entry name、container rootfile 和 manifest href 全部规范化，拒绝 absolute、`..`、反斜杠和重复路径；
- `mimetype` 必须是第一个 Stored entry 且逐字为 `application/epub+zip`；
- entry 数、单 entry/总解压字节、XML token/depth 和正文预算有硬上限；
- manifest ID/href 唯一，spine idref 必须存在且为 XHTML；线性顺序完全等于 spine；
- nav/NCX 只提供章节标签与内部目标，不改变 spine；外部 URL 不进入内部 Reference；
- XHTML 使用 strict XML 解析；inline 文本规范化，block 结构不退化成固定长度切片；
- `table → row → cell`、`aside/epub:type=footnote` 和 `a/epub:type=noteref` 映射到 F192 强类型；
- 每个 manifest resource 都进入 IR Resource 清单并绑定解压后 bytes/SHA-256；
- v1 不执行 CSS layout、JavaScript、字体、音视频或网络请求，unsupported/malformed 输入返回稳定错误码。

## RED 与完成门

- RED 先证明 EPUB source port/config/adapter/receipt API 不存在；
- 冻结 EPUB3 fixture 的 ZIP 顺序故意不同于 spine，输出章节与 reading order 必须遵循 spine；
- nav 标题、两列表格、脚注/noteref、图片资源和 source anchor 全部进入有效 IR；
- 同一 Source 重复解析产生相同 Document/Resource/Node/IR digest；
- EPUB2 NCX fallback 有独立 fixture；
- mimetype、container、OPF、manifest/spine、traversal、duplicate、malformed XHTML 和 zip budget 负例；
- context 取消、Reader 关闭、SourceStore corruption 透传；目标测试、`-race` 和完整 CI 全绿。

用户执行授权：2026-08-05 用户要求持续执行至 F204。

开工前结论：PASS。
