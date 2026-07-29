# Obsidian Wiki 导出

状态：单向导出方向已形成；文件命名和同步范围未确认。

## 定位

Memora 数据库是权威状态。Markdown Wiki 是某个一致 MVCC 快照的确定性可读投影。

```text
Semantic Record → Markdown 页面
Router Page      → Index.md / MOC
Relation         → [[Wikilink]]
Database         → 一级目录
Route            → 子目录
```

每个语义记录天然成为一个短小 Wiki 页面，适合 AI 和人阅读。

物理 Tablespace、Data File、Page、Slot、Segment 和 Record 位置不能成为 Wiki 页面的身份。一个可导出的语义实体 `row_id` 对应一个 Markdown 页面；revision 变化只更新该页面，历史版本默认不分别导出。

“一个数据项一个文件”只针对人和 AI 会直接阅读的语义实体。关系、Posting、Schema 版本、Undo 等内部记录不单独生成 Markdown，而是投影为页面链接、属性或索引页，避免导出成海量无意义小文件。

## 第一阶段边界

- 单向导出到 Obsidian Vault；
- 不从手工修改的 Markdown 静默回写数据库；
- 默认只导出当前有效状态；
- 相同快照和配置产生稳定结果；
- 使用内部稳定 ID 解析链接，不通过标题猜目标；
- frontmatter 保存最少的 ID、database、table、route 和 revision。

## 为自主 Schema 预留导出语义

字段名由 AI 决定，导出器不能假设每张表都叫 `title` 或 `content`。Table Schema 需要保存独立的 Export Profile：

```text
title_field_id
body_field_ids[]
property_field_ids[]
tag_field_ids[]
hidden_field_ids[]
field_order[]
```

Profile 引用稳定 `field_id`，所以字段改名不会破坏导出。AI 建表或改表时同时维护 Profile；缺失时使用确定性的通用字段列表作为降级输出。

## 文件身份和链接

- 文件名必须带稳定 ID 或稳定 ID 后缀，不能只使用可变标题；
- frontmatter 保存完整 `row_id` 和 revision；
- 关系在数据库中始终引用 `row_id`，导出时才解析为 `[[path|title]]`；
- 导出先生成 `row_id → path` 清单，再生成正文和链接；
- 标题、Route 或 Table 改变时由 manifest 处理移动，不能生成第二份对象；
- split/merge 创建明确的新旧对象关系，不能靠文件名猜继承关系。

## 增量导出

manifest 记录 `object ID → revision → path → content hash`。只重写变化记录、变化 Router 和受链接变化影响的页面。

## 未决问题

- 中文标题还是英文 slug；
- 同名页面如何生成稳定路径；
- 默认文件名采用纯 ID，还是 `slug--short-id`；
- 页面移动后是否生成 redirect stub；
- 是否导出 Schema 页面；
- 关系过多时页面底部如何限流；
- 部分 Route 导出时怎样处理跨范围链接；
- 是否永远不支持双向同步，还是以后使用显式 diff/plan 导入。

## 关联

- [语义记录](../data/semantic-records.md)
- [语义路由](../query/semantic-routing.md)
