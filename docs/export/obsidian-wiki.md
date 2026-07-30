# Obsidian Wiki 导出 v1

状态：F45 已冻结并实现；单向、确定性导出，不支持回流。

## 定位

Memora 数据库是权威状态。Markdown Wiki 是某个一致只读 snapshot 的可读投影：

```text
当前有效 Row → Markdown 页面
Relation       → [[稳定 ID 路径|当前标题]]
Database/Table → 稳定 ID 目录
```

一个可导出的语义 `row_id` 对应一个页面；revision 变化只更新该页面，历史版本默认不展开。物理 Page、Record、Posting、History、Router 和 Undo 等内部记录不单独生成页面。Router/MOC 与部分 Route 导出不属于 v1。

## 第一阶段边界

- 单向导出到 Obsidian Vault，不把 Markdown 当第二份真相源；
- 默认只导出当前有效 Row；
- 相同 snapshot 和 Profile 产生稳定结果；
- 使用完整 Row locator 解析关系，不通过标题猜目标；
- frontmatter 保存版本、Row/Database/Table 身份、当前名称、revision 和可选 tags；
- 导出前获取一次只读逻辑 snapshot，不混入导出期间的后续提交；
- 不调用 LLM，不在导出时重新解释或改写内容。

## 命令

CLI 是参数化 MSQL 的便捷入口：

```text
memora export --wiki /absolute/vault --profile profile.json
```

```sql
EXPORT WIKI TO :path PROFILE :profile;
```

`:path` 必须是绝对、规范化路径；`:profile` 是 JSON 文本。语句只允许在 autocommit 会话执行，结果返回 `path`、`snapshot_sha256`、`profile_sha256` 和 `object_count`。

## Export Profile

Profile 版本固定为 `memora.wiki-profile/v1`。Table key 和所有字段引用均使用稳定 ID：

```text
title_field_id
body_field_ids[]
property_field_ids[]
tag_field_ids[]
hidden_field_ids[]
field_order[]
```

title、body、property、tag、hidden 角色不能重复。`field_order` 非空时必须恰好排列全部 body/property 字段。tag 字段的非空文本值按字段顺序写入 YAML tags。

字段 rename 不改变角色。未配置的 Table 使用确定性降级：第一个非空 TEXT 值作为标题，其余字段按 Schema 顺序写正文。v1 接收调用方提供的 Profile；由自主 Schema 自动生成并持久化 Profile 仍待后续设计。

## 文件身份和链接

- 路径固定为 `<database_id>/<table_id>/<row_id>.md`；
- ID 中不适合文件名的字符替换为 `_`，并附加原 ID SHA-256 的 8 位前缀；
- frontmatter 保存完整 `row_id` 和 revision；
- Database、Table、Column 或标题 rename 只更新内容，不移动文件；
- 跨 Database 关系使用 Vault 根目录下的完整稳定相对路径；
- 导出先生成全部 Row locator 到 path/title 的清单，再生成正文和 Wikilink；
- split/merge 创建明确的新旧对象关系，不能靠文件名猜继承关系；
- v1 不生成可变 slug 路径或 redirect stub。

## 增量导出

Vault 根目录的 `.memora-wiki.json` 记录版本、snapshot/profile 哈希，以及每个对象的 Database/Table/Row ID、revision、path 和内容哈希。

导出器只重写内容变化的页面。所有新页面先写入，再删除上一份 manifest 拥有但当前不存在的旧页面，最后原子替换 manifest。未被 manifest 登记的用户文件不会删除；对导出页面的手工修改会在下一次内容有差异的导出中被覆盖。

## 未决问题

- 是否导出 Schema 页面；
- 关系过多时页面底部如何限流；
- Router/MOC 与部分 Route 导出怎样表达，跨范围链接怎样降级；
- 是否永远不支持双向同步，还是以后使用显式 diff/plan 导入。

## 关联

- [语义记录](../data/semantic-records.md)
- [MSQL](../query/msql.md)
