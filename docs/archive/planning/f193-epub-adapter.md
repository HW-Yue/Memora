# F193：EPUB 确定性适配器

状态：已完成，2026-08-05。

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

## 完成证据

- EPUB3 fixture 的 ZIP 顺序为 ch1/nav/ch2、spine 为 ch2/ch1，输出稳定为“第二章→第一章”；
- container、OPF 2/3、manifest、spine、EPUB3 nav 和 EPUB2 recursive NCX 均经 bounded strict XML；
- inline 前/中/后文本保持原顺序，chapter/heading/paragraph/list/table/row/cell/image/footnote 映射
  通过 F192 Seal，noteref 精确指向 footnote Node；
- 所有 manifest resource 记录 logical locator、解压 bytes/SHA-256；Node anchor 不越过对应 XHTML；
- 重复解析逐字一致；mimetype、container、traversal、duplicate、missing idref、坏 XHTML、未知 OPF
  version、entry budget 和非法 nav href 均有负例；
- context 取消、SourceStore corruption 和 Source close failure 均透传，Reader 必定关闭；
- 目标测试与 `-race` 全绿，解析只使用标准库 ZIP/XML，不执行脚本、网络、CSS 或模型调用。

完成门结论：PASS。下一项为 F194 ReadExtent 与 coverage 调度。
