# Tablespace、Page 与 Record 布局

状态：B+ Tree、16 KiB Page、单实例 Buffer Pool 与 Redo WAL 已确认为 F81 必做；
完整 per-table Tablespace/Extent 仍是后置候选。见
[ADR-0006](../decisions/0006-mysql-page-buffer-wal-cow.md)。

## 核心分离

不要使用“一张 Table 必须对应一个 Data File”的硬编码布局。先使用标准物理层次：

```text
Memora Instance
  → Tablespace
    → Data File
      → Page

Page 按 Extent 批量分配
Segment 为具体索引或存储结构持有 Page/Extent
```

Table/Row 是逻辑身份。F81 B+ Tree 将稳定 `row_id` 映射到当前/可见 immutable
revision locator；长期语义 revision 进入独立 History。未来若采用 in-place Record
与物理 Undo chain，仍不能与永久 History 混用。

完整候选布局中，每个 User Table 使用不可变 `table_id` 的独立 Tablespace；F81
是否先共用 Database space 由实现复杂度决定。当前 Row、聚簇索引和二级 B+ Tree
可分配独立 Segment；Router physical generation 与业务 Row 身份保持分离。

## 第一版尺寸

- Page：16 KiB，已确认；
- Extent：1 MiB，即 64 个 Page；
- Data File 滚动上限：256 MiB，即 256 个 Extent；
- 编码后单条 Record 的页内目标：不超过 8 KiB；
- 语义 Row 启动写作目标约 800 字，文本 Column 启动默认上限为 1200 个字符并可演化；字段超限由逻辑写入层报错，物理 Page 层不负责截断。

除 Page 外其余值仍是候选。16 KiB 对语义 Row 和系统元数据有余量；256 MiB
滚动粒度必须通过基准测试确认。

## 按 Page 定位读取

正常查询禁止把整个 Data File 读入内存。引擎根据 `page_id` 计算 Tablespace、Data File 和字节偏移，只用 `ReadAt/pread` 读取需要的 Page，再放入有严格容量上限的 Buffer Pool。第一版不依赖整文件 mmap。

即使 Data File 很大，普通查询也只沿索引读取少数 Page。全扫描必须流式读取，不能按文件大小申请内存。

## Page 结构

Page 固定大小，内部使用 slotted-page：

```text
Page Header
Slot Directory  → Record 偏移和长度
Free Space
Record Bytes
Checksum / Page LSN
```

Slot 可移动而逻辑 `row_id` 不变。B+ Tree 叶子优先只保存紧凑键、`row_id` 和 Record 定位，不内联完整可变正文。

Record 变大且当前 Page 空间不足时，引擎可以迁移并更新稳定定位，不在原位置强行
扩张；旧语义 revision 由 immutable version/History 保留，不假设已存在物理 Undo。
B+ Tree 只对键 Page 执行 split/merge，正文增长不会改变逻辑 `row_id`。

## Segment 的标准含义

Segment 不是 Data File。它是为某个结构分配的一组 Page/Extent，例如 B+ Tree 的 leaf segment 和 non-leaf segment。小 Segment 可以逐 Page 增长，变大后按 Extent 分配。

倒排索引的不可变批次称 `Posting Run`，避免和 Tablespace Segment 混用。

## Column 采用混合布局

- Bool、整数、浮点、时间和固定标识使用定长编码；
- Text、Bytes、数组和可选结构使用变长编码；
- Null Bitmap 表示空值；
- 变长 Column 由 offset/length 目录定位，字符串统一 UTF-8；
- 超过页内阈值的极少数值使用 overflow page。

overflow 只是代码片段、表格等异常记录的安全阀，不是保存 PDF、大文档或机械 chunk 的入口。明显过大的语义记录仍应由 AI split。

## Schema 演化

- 每个 Column 有稳定 `column_id`，改名不改变 ID；
- 删除后的 `column_id` 永不复用；
- 每条 Record 携带 `schema_version`；
- Data Dictionary 保存各版本的 Column 顺序、类型和默认值；
- 旧 Record 按原 Schema 解码，读取时投影到当前 Schema；
- 类型变化通过显式迁移生成新 Record，不能静默重解释旧字节。

候选编码为“固定系统头 + Null Bitmap + fixed area + varlen directory + payload”，不在每条 Record 重复保存完整 Column 名。

## 与 Markdown 导出的关系

Tablespace、Data File、Page、Slot、Segment 和 Record 地址都不得进入 Markdown 身份。导出只依赖稳定 `row_id`、当前 revision、逻辑 Column 和关系。

一条可导出的语义 Row 即一个 Markdown 页面；同一 Row 的 revision 更新同一页面。关系、索引和 Undo Log 等内部 Record 不单独生成页面。

## 尚未确认

- 1 MiB Extent 和 256 MiB Data File 策略是否合适；
- Row Directory 主键和 Record 指针的具体编码；
- 8 KiB 页内阈值以及 overflow 的硬上限；
- Table Data File 的滚动大小、编号、预分配和回收策略；
- `pread/pwrite` 稳定后是否增加局部 mmap；
- checksum、压缩和加密的粒度。

## 关联

- [存储引擎术语](./terminology.md)
- [物理与检索索引](./indexing.md)
- [语义记录模型](../data/semantic-records.md)
- [Obsidian Wiki 导出](../export/obsidian-wiki.md)
