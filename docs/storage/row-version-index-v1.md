# Row Version Index v1

状态：F100 已完成；F103 已追加 snapshot 契约；F106 已追加空树完整历史 Bootstrap。

## 唯一结果

历史 Row revision 通过持久化 B+ Tree 按 Row ID + revision 精确定位，也能按
Row ID + commit sequence 找到不晚于目标 sequence 的最后一个 revision。查询不枚举
其他 Row 或从该 Row 的历史头部线性扫描；not-found/corruption 不回退旧 Record 扫描。

索引保存 immutable revision locator，不保存 Row 正文、不提供 Table 分页。F102 已把
AS OF/point-get 接入，F103 以持久 high-water 为 statement/transaction read view。

## 键空间

- revision key：类型前缀 + Row ID + 大端 revision，用于精确 Get；
- commit key：类型前缀 + Row ID + 倒序 commit sequence + 倒序 revision；
- identity key：类型前缀 + Row ID，固定该 Row 的 Database/Table 身份。
- legacy anchor key：类型前缀 + Row ID，保存离线导入的最高 sequence 0 revision；
- singleton snapshot high-water key：保存已完整发布的最大正 commit sequence。

commit key 的两个倒序整数使 forward cursor 从目标 sequence 开始读取一项，就得到
最大 commit <= target；同一 Row 在同一 commit 有多个 revision 时，返回最高 revision。
键使用长度前缀，ID 精确匹配，单组件最多 2048 UTF-8 bytes。

## Immutable Locator

Locator v1 包含 Database ID、Table ID、Row ID、Schema revision、Row revision、
commit sequence 和 live/deleted/superseded 状态。Schema/Row revision 必须非零。

F17 前兼容 revision 可以有 commit sequence 0：它写 revision key 与 legacy anchor，
不写 commit key，因此可按 revision 或 snapshot fallback 读取，但不会伪装成 AS OF
时间线位置。所有 legacy locator 必须在 F106 的空树离线 `Bootstrap` 中完成导入。

## 追加与冲突

- Append 可在一个 F97d3 事务中追加多个 locator，每个 positive-sequence locator
  同时写 revision/commit key；
- 同 Row 的 Database/Table identity 永久不变；
- 已存在相同 locator 是无 WAL 幂等成功；
- locator、legacy anchor 与 high-water 在同一个 WAL transaction 原子发布；
- marker 建立后，新 locator 的 positive sequence 必须严格大于旧 high-water；迟到历史
  与新 sequence 0 revision 在 WAL 前整批冲突，已有 locator 幂等重放除外；
- 相同 Row + revision 指向不同 locator、同批重复 revision 或 identity 漂移均在
  WAL 前整批失败；
- 索引只追加，不覆盖或删除历史 key。

`Bootstrap` 与普通 `Append` 分离：它只接受空树，把全部 immutable locator、legacy
anchor 与 high-water 在一个 WAL 事务中发布；空历史也会创建带 high-water 0 的非零 root。
完成后历史封存规则立即生效，重复 Bootstrap 稳定冲突。

## 完成证据

- exact revision、exact commit、AS OF floor、same-commit highest revision；
- sequence 0 不支持 commit lookup，只支持 exact revision 与 snapshot legacy fallback；
- high-water/legacy floor、固定 snapshot、迟到历史封存与 marker corruption；
- immutable/identity/batch 冲突在 WAL 前失败，旧历史保持；
- 大历史 split 后与 reference model 一致；
- 并发相同 revision 只有一个提交；
- crash-before-flush reopen、Locator/tree corruption、F99/F98/F97d3 回归；
- targeted repetition、race、全仓 CI 通过。
