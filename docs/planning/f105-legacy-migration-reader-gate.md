# F105 Legacy Store Migration Reader 开工与完成门

状态：已完成，PASS。

## 产品门

- 目标故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；升级前能证明将迁移哪些
  Catalog/Row revision，坏源或遗漏不会进入新 authority。
- 标准旅程：停止 writer → inventory → decode/validate → 得到带 source/plan digest
  的只读 Plan → F106 才 apply。
- 作用范围：legacy `.memora` committed Record 与 F98–F100 bootstrap input；不改变语义。
- 上下文：纯离线引擎元数据，不进入 Agent Route Frame。
- 永久边界：SQLite migrator、immutable body、Page index 与 logical authority 不混淆。
- 架构选择：全 Record ref 指纹 + Catalog/全部 Row revision 结构校验；前后双 inventory。
- 用户执行授权：F81–F109 持续授权已记录。
- 唯一主要结果：从未修改的 legacy source 生成完整且可重验的 Page-index Plan。
- 明确不做：Page/WAL/root 写入、备份、切权、Route/Relation Page 索引与在线迁移。
- 开工前结论：PASS。

## RED matrix

| Case | 当前缺口 | 期望 |
| --- | --- | --- |
| committed inventory | File 只能按已知 kind 分别取 IDs | 全 kind 有序 ref/count/fingerprint |
| deterministic plan | 无 F98–F100 bootstrap plan | reopen/重复读取 byte-stable digest |
| full versions | Repository 只公开 latest Rows | 全 revision 连续、排序、current 可推导 |
| corruption | 错 body/identity 可能晚到查询才发现 | 规划时失败且零部分 Plan |
| source change | inventory 与 apply 间无绑定 | 前后及 apply 前指纹变化稳定拒绝 |
| unknown kind | 新 kind 可能被静默忽略 | unsupported 阻断 |
| cancellation/read-only | 长 inventory 可能继续或写源 | 及时取消，文件 bytes/size 不变 |

首个 RED：包含两版 Row 的 legacy file 当前没有只读 `Build` 能返回 source digest、
current locator 与两条 version locator。

## 完成门

- Record ref 排序/golden、committed-only、reopen 与 unknown-kind；
- empty/single/large Catalog+Rows、全部 revision reference model、deterministic digest；
- CRC/codec/identity/gap/sequence corruption，source-changed 与 cancellation；
- 源文件 hash/size 不变、targeted repetition、全仓 unit/vet/race/CI；
- 完成后结论：PASS。首个 RED 仅因 `Reader.Build` 缺失失败；补充的 apply 前校验
  RED 仅因 `Reader.VerifySource` 缺失失败。

## 验收证据

- `File.Records` 只枚举 committed Record，按 kind/ID 稳定排序并保留 schema/length；
- empty、两 revision、1200 Row/1500 revision reference model、重复读取与 reopen 通过；
- unknown kind、CRC、revision gap、sequence 回退、identity drift、调用方篡改和源变化均拒绝；
- Reader 前后盘点，`VerifySource` 在 F106 apply 前再次核对 fingerprint/count；源文件
  SHA-256 保持不变；
- 相关包 `-count=5`、相关包及全仓 `-race`、全仓 unit/vet 和 `scripts/ci.sh` 全绿。
