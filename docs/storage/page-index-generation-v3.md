# Page Index Generation v3

状态：F173b1 已完成；本规格是当前新写 generation 格式，v1/v2 只读兼容用于自动收敛或升级。

## 结果

Generation v3 保持 Catalog、Current Row、Row Version、Fulltext 四树布局，但 Fulltext seed 从
Catalog/Row 扩展为当前 Catalog、当前 live Route 和当前 Row 的完整 lexical 派生位置。

## 固定布局

```text
page-index-v1[.g<epoch>.<plan-digest>]/
├── manifest.json
├── catalog.pages / catalog.wal/
├── current.pages / current.wal/
├── versions.pages / versions.wal/
└── fulltext.pages / fulltext.wal/
```

目录前缀继续为 marker 兼容保留；实际格式由 manifest version 判断。Fulltext Space ID 仍为
`MEMFTX`，object/owner/posting key space 不变。

## Plan v3 与构建

Migration Plan v3 绑定 Catalog、所有 Row version locator、current locator/body，以及按 Route ID
排序的当前 live Route。Route snapshot 必须属于当前 Catalog Table，形成合法单根父树，并与同一
native inventory fingerprint 前后绑定；Plan digest 覆盖全部内容。

Fulltext seed 顺序为：

```text
Catalog documents + Route documents + Row documents
```

四树继续在隐藏 staging 中 bootstrap、flush、sync、strict reopen 与 reference 校验，再执行 source
reverify、atomic rename 和父目录 fsync。初始一致性来自整个 generation 目录一次发布，不伪装成跨
WAL 事务。

## v1/v2 compatibility

- v1 三树 manifest 可打开后通过 COW 生成 v3；
- v2 四树 manifest 可 fail-closed 打开，缺失 revision-one Route 可增量补齐；
- v2 中 Route first revision 大于 1 或存在 revision gap 时，用当前 Plan COW 生成 v3；
- 旧 generation 损坏始终拒绝，不能借升级绕过；COW 后旧目录保留到显式回收。

版本号本身不强制 COW；是否重建由当前派生对象能否按 revision contract 收敛决定。

## 关联

- [Generation v2](./page-index-generation-v2.md)
- [F173b1](../planning/f173b1-route-posting-generation.md)
- [F171 Posting Store](../planning/f171-persistent-posting-store.md)
