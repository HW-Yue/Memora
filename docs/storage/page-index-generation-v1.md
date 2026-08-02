# Page Index Generation v1

状态：历史兼容格式；F106–F108 已完成。新写格式由
[Generation v3](./page-index-generation-v3.md)取代，v1 仅保留只读升级能力。

## 唯一结果

把 F105 的有效 Plan 构建为一套完整、可重开验证、原子发布的 Catalog/current
Row/Row version Page 索引。整个 generation 在隐藏 staging 目录生成；只有全部树和
source binding 通过后才 rename 为 `page-index-v1/`。legacy `.memora` body 始终不改。

## 固定布局

Database 目录下的候选 generation：

```text
page-index-v1/
├── manifest.json
├── catalog.pages
├── catalog.wal/
├── current.pages
├── current.wal/
├── versions.pages
└── versions.wal/
```

三棵树使用冻结且互异的 Space ID（`MEMCAT`、`MEMCUR`、`MEMVER`）。各自仍遵守 F97
Tree Runtime 的 Page/WAL/control 协议；跨树原子性来自 staging generation 的目录发布，
不伪造一个跨 WAL 事务。

## Apply

1. 校验 Plan digest，并用 F105 Reader 重验 source fingerprint/count；
2. 在目标父目录创建唯一的隐藏 staging 目录；
3. 从空 Page/WAL 分别 bootstrap Catalog、最终 current locator 和全部 version locator；
4. flush/sync/close，reopen recovery 后逐项验证 lookup、history、high-water 与 Catalog；
5. 计算 staging 内除 manifest 外全部普通文件的 canonical SHA-256；
6. 写入并 fsync manifest，再次重验 source；
7. 原子 rename 为 `page-index-v1/` 并 fsync 父目录。

发布前任一步失败只清理本次确定创建的 staging；legacy Store 仍是完整 authority。
rename 后父目录 fsync 失败属于 outcome unknown：不删除已可见 generation，重试通过
manifest、内容 digest 和 Plan binding 收敛。

## Manifest 与重验

`memora.page-index-generation/v1` 保存 Plan version/digest、source SHA-256、三棵树的
kind/Space ID/Page/WAL 名称、最终 root state、content SHA-256 和自身 canonical digest。
读取时拒绝未知字段、路径漂移、重复/缺失 tree、非普通文件、symlink、内容或状态变化。

相同 Plan 重试会再次 fsync 父目录并返回 idempotent receipt；已存在 generation 若绑定其他 Plan 返回 conflict，
若 manifest、Page、WAL 或 locator 验证失败返回 corruption。F106 只发布候选 generation；
daemon/MSQL 默认读写切换、写锁接线和旧扫描不可达证明属于 F107。

## 完成证据

empty 与 1000 Row/1200 revision generation 均通过逐 locator 验证和 reopen；全部发布前
phase fault、source change、并发发布、rename/sync outcome unknown、manifest/Page/WAL
corruption、全仓 race 与 CI 已覆盖。

## 关联

- [Page Index Migration Plan v1](./page-index-migration-plan-v1.md)
- [Durable Tree Runtime v1](./durable-tree-runtime-v1.md)
- [Page File Manager v1](./page-file-manager-v1.md)
