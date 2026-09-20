# F202：文本层 PDF 确定性适配器

状态：已完成；2026-08-05 规格已冻结并通过完成门。

## 唯一主要结果

实现 `SourceStore text-layer PDF → Document IR v1` 的确定性适配器。适配器按 PDF Page tree 顺序
读取页面，解析受支持的 content stream、text operator 和字体 Unicode 映射，输出逐页语义文本节点。
F202 不做 OCR、不猜图片中文字、不调用模型，也不把视觉坐标启发式冒充段落或表格结构。

## v1 支持面

- PDF 1.4–1.7、classic xref table、单 revision、未加密 indirect objects 与 Page tree；
- page 继承/自有 Resources，Contents 单 stream 或有序 stream array；
- 无 Filter 或单个 `FlateDecode` content/ToUnicode stream，并有解压预算；
- `BT/ET`、`Tf`、`Td/TD/Tm/T*`、`Tj/TJ/'/"` 文本操作；reading order 等于 content stream
  操作顺序，不按 object number、xref 顺序或二维坐标重新猜测；
- Type1 WinAnsi/ASCII 字体；Type0/CID 字体必须提供受限 `ToUnicode` bfchar/bfrange CMap；
- 每页映射为 `part`，每个确定性文本行映射为 `paragraph`；anchor 指向原 PDF 中对应 page/content
  object span，并带 `pdf:page=N;stream=X Y R;line=N` selector；
- PDF 本身是唯一 Document Resource；Info Title/Catalog Lang 可选，缺失时使用文件名与 `und`。

## 明确拒绝

加密、xref/object stream、incremental Prev、多级 Filter、LZW/ASCII85、缺失字体、无 ToUnicode 的
Type0、自修改/脚本、坏引用/循环 Page tree、超预算，以及零可读文本页均 fail closed。后者返回明确
“需要 OCR/视觉路径”的证据，不在 F202 内自动回退。

PDF 的表格在没有 Tagged PDF 结构时不存在确定性 cell 语义。v1 冻结一个表格外观样本，只验证
`TJ`/换行的操作顺序无丢字，输出仍是 paragraph；不得用空格或坐标阈值伪造 `table/row/cell`。

## 完成门

RED 冻结一个两页 classic-xref PDF：ZIP/object 顺序无关，Page tree 为第二页→第一页；包含 Tj、TJ、
FlateDecode、WinAnsi 与 ToUnicode 文本。输出页序、文本、object-span anchor 和 digest 必须稳定。
负例覆盖 encrypted、xref stream、Prev、坏 xref/ref/Page cycle、unsupported filter/font/operator、扫描页、
file/page/object/stream/text budget、context cancellation、Source corruption 与 close failure。

目标 package、race、vet、Agent import boundary 和完整 CI 全绿；不执行真实模型或答案质量评测。

用户执行授权：2026-08-05 用户要求持续执行至 F204。

## 完成证据

- RED：`internal/agent/pdf_adapter_test.go` 先在缺少适配器时编译失败；GREEN 后覆盖稳定页序、Tj/TJ、Flate、WinAnsi、ToUnicode、anchor/digest 重复解析和负例；
- 负例包含加密、Prev、xref stream、坏引用/Page cycle、LZW、缺失 ToUnicode、未知文本操作、扫描页、页数预算、取消、SourceStore corruption 与 close failure；
- `go test ./internal/agent -count=1`、`go test ./internal/agent -race -count=1`、`go vet ./internal/agent` 通过；没有真实模型调用或批量质量评测。

## 关联

- [Document IR v1](./f192-document-ir-v1.md)
- [DOCX 确定性适配器](./f201-docx-adapter.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
