# Current Row Index v1

状态：F99 已完成；格式与行为契约已冻结。

## 唯一结果

稳定键 table_id + row_id 通过持久化 B+ Tree 精确定位该 Row 最新已提交
revision。点查只沿 root-to-leaf 路径读取，不枚举其他 Row 或该 Row 的历史
revision；not-found/corruption 不得回退到旧 Record ID 扫描。

F99 不保存 Row 正文、不提供历史/as-of 查找、不切换 MSQL Executor。F100 增加
immutable revision 索引，F102 才切 point-get，F107 才切 Page Store authority。
F99 返回的 Row ID + revision 是 F100 immutable record lookup 的确定性输入。

## 键与 Locator

键使用版本前缀和大端长度前缀编码 Table ID、Row ID。两者精确匹配、不 lowercase，
单个 UTF-8 组件最多 2048 bytes，空值、首尾空白或超限均拒绝。

Locator v1 是严格的版本化二进制值：

- Database ID、Table ID、Row ID；
- 当前 Schema revision、Row revision、commit sequence；
- live、deleted 或 superseded 当前状态。

Schema/Row revision 均非零。F17 之前的兼容 Row 允许 commit sequence 为 0，明确
表示历史缺口；它的下一次 mutation 必须推进到正 sequence。键 scope 与 Locator
Table/Row 不一致、未知版本/状态、非法层级、保留位非零、尾随字节或 UTF-8 损坏
均视为 corruption。

## 原子更新

一次 Apply 接收一个或多个 Current Row update：

- insert 使用 expected_revision = 0，新 Row 必须从 revision 1 开始；
- mutation 使用当前 revision 作为 expected，新 revision 必须恰好加一；
- 同一 Row 的 commit sequence 必须严格增加，Schema revision 不得倒退；
- 同批次重复键、已存在 insert、stale expected 在写 WAL 前整批失败；
- 当前 Locator 已与目标完全相同时是无 WAL 的幂等成功；
- 验证通过后按键排序，由 F97d3 在一个 WAL 事务中发布全部叶页和 root。

逻辑删除和 split/merge supersede 保留为“当前 Locator”，不物理删除 key，供 revision
guard、History 和后续 Table Cursor 判断可见状态。

## 完成证据

- insert/update/delete/supersede、not-found、scope 与 revision guard；
- stale/duplicate/bad transition 在 WAL 前失败且旧 Locator 保持；
- 多 Row 原子 batch、并发 same-base 只有一个结果提交；
- 大量 Row 触发 split 并与 map reference model 一致；
- crash-before-flush 后 reopen 恢复 committed root；
- Locator/tree Page corruption 无 scan fallback；
- F98/F97d3 回归、targeted repetition、race 与全仓 CI 通过。
