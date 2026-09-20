# B+ Tree Range Cursor v1

状态：F92 已完成，2026-07-31；冻结有界 forward cursor 与 leaf-link 续读契约。

- `NewCursor(start_inclusive, end_exclusive)` 固定深复制边界；空边界表示无界；
- start 不为空时按 F91 separator 规则只下降到一个起始 leaf；空 start 走最左路径；
- `Next(limit)` 返回最多 limit 个严格升序的深复制 `key/value`，limit 必须大于 0；
- Cursor 在 batch 间保存 leaf Page、slot 与已访问 Page ID，不重复从 root 查找；
- end 是 exclusive；到 end、leaf chain 尾或空 root leaf 时返回 `Done=true`；
- batch 恰在 Page 尾结束且 `next_leaf_page_id != 0` 时可保守返回 `Done=false`，
  下一 batch 再读取下一 Page；
- 后继 Page 必须是同 space、指定 Page ID、level 0 leaf，且首 key 严格大于前叶末 key；
- 非 root 空 leaf、leaf cycle、错 identity/type/order、超过 64 层的初始下降均为 corruption；
- Reader fault 或 corruption 使 Cursor poison，当前 batch 不返回部分结果，后续稳定返回同一错误。

F92 不提供 reverse cursor、snapshot/MVCC、prefetch、mutation、split 或删除。
