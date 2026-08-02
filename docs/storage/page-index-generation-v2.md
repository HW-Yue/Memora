# Page Index Generation v2

状态：F172a 已批准；本规格取代 v1 作为新写 generation 格式，v1 只读兼容用于升级。

## 结果

Generation v2 在 v1 的 Catalog、Current Row、Row Version 三棵树之外增加 Fulltext Tree，保证
激活时所有当前 Row 都有可重开的 lexical 派生位置。在线发布由 F172b 接入。

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

Migration Plan v2 绑定 Catalog、所有 version locator、current locator 和当前 Row body。四棵树在隐藏
staging 中分别 bootstrap、flush、sync、strict reopen 与 reference 校验；随后 content digest、manifest
digest、source reverify、atomic rename 和父目录 fsync 沿用 v1 协议。

四棵树不伪装成跨 WAL 事务：初始一致性来自整个 staging 目录一次发布；live mutation 的 publication
barrier、poison 和 reopen reconciliation 由各接入 Feature 负责。

## v1 compatibility

合法 v1 manifest 和三树 inventory 仍可 fail-closed 打开，但只能作为升级源。Authority 用现有 COW
replacement 生成 v2 并原子切 marker；不向 v1 目录原地补文件。损坏 v1 仍拒绝，不能借升级绕过校验。

## 关联

- [Generation v1](./page-index-generation-v1.md)
- [F172a](../planning/f172a-row-posting-generation.md)
- [F171 Posting Store](../planning/f171-persistent-posting-store.md)
