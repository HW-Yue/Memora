# Page Index Generation v2

状态：已被 [Generation v3](../../storage/page-index-generation-v3.md) 取代；保留为 v2 兼容格式说明。

## 结果

Generation v2 在 v1 的 Catalog、Current Row、Row Version 三棵树之外增加 Fulltext Tree，当时保证
激活及在线 Row revision 发布后，所有当前 Row 都有可重开的 lexical 派生位置。

## 固定布局

```text
page-index-v1[.g<epoch>.<plan-digest>]/
├── manifest.json
├── catalog.pages / catalog.wal/
├── current.pages / current.wal/
├── versions.pages / versions.wal/
└── fulltext.pages / fulltext.wal/
```

目录前缀为 marker 兼容而保留；权威格式由 manifest version 判断。Fulltext Space ID 固定 `MEMFTX`，
其 object/owner/posting key space 见 F171。

## 构建与发布

Migration Plan v2 绑定 Catalog、所有 version locator、current locator 和当前 Row body。该版本四棵树在隐藏
staging 中分别 bootstrap、flush、sync、strict reopen 与 reference 校验；随后 content digest、manifest
digest、source reverify、atomic rename 和父目录 fsync 沿用 v1 协议。

四棵树不伪装成跨 WAL 事务：初始一致性来自整个 staging 目录一次发布；F172b 的 live Row mutation
按 Version → Fulltext → Current 发布，并由 barrier、poison 和 reopen reconciliation 收敛。

## v1 compatibility

合法 v1 manifest 和三树 inventory 仍可 fail-closed 打开，但只能作为升级源。Authority 用现有 COW
replacement 生成四树 generation 并原子切 marker；当前新 build 写 v3，不向 v1 目录原地补文件。
损坏 v1 仍拒绝，不能借升级绕过校验。

## 关联

- [Generation v1](./page-index-generation-v1.md)
- [F172a](../../planning/f172a-row-posting-generation.md)
- [F171 Posting Store](../../planning/f171-persistent-posting-store.md)
