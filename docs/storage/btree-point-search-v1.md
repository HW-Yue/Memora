# B+ Tree Point Search v1

状态：F91 已完成，2026-07-31；冻结只读 root-to-leaf 精确查找契约。

- Searcher 固定 `space_id + root_page_id`，通过注入 Reader 按 Page ID 读取；
- 每个 Page 的 SpaceID/PageID 必须与请求一致，并通过 F90 Decode；
- internal separator 是右 child 的下界：`key < separator` 走左侧，等值走右 child；
- child level 必须恰好比 parent 小 1，最终必须到 level 0 leaf；
- leaf 对有序 key 做二分，命中返回深复制 value，未命中返回 `ErrNotFound`；
- 搜索只读取单条 root-to-leaf 路径，不预取 sibling 或扫描其他 leaf；
- 重复 Page ID、超过 64 层、错 identity/type/level 均归类为 corruption；
- Reader I/O error 保留错误链，不伪装 not-found。

F91 不实现 range、leaf chain、insert/delete、root 持久化或 Buffer Pool 接线。
