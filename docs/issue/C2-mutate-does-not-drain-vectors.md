# C2 · `memora mutate` 不排干向量，而 `write.md` 让你优先用 `mutate`

严重度：high（静默的派生层缺口：写全成功，向量永远缺）。状态：**待做**（已在主线上复核仍然如此）。

## 症状

按 Skill 推荐的写法写行，向量永远是缺的：写全成功，`SHOW PENDING VECTORS` 里堆着，直到有人手工排。

## 证据

- Skill 两边说法冲突：
  - `references/recall-and-vectors.md`：配了 provider 的宿主，**`memora exec` 会替你排干**。
  - `references/write.md`（"Send the write through `memora mutate` when you can."）让人**优先用 `mutate`**。
- 实现：`internal/cli/cli.go` 的排干只在 `command == "exec" && len(statements) == 1` 时触发，
  **`mutate` 那条路径没有**。
- 实测：建第一个配置库时那 5 行就是手工排的（5 个单元，`embed_query.py` + 一批 `ACCEPT VECTOR`）。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) C2。

## 已定的修法（二选一，修法一更对）

1. **首选**：让 `mutate` 也调 `drainAfterWrite` —— 否则"推荐路径"和"自动排干"永远不重合。
2. 或者在 `write.md` 里明确写「`mutate` 之后必须自己排干」。

## 坑

若走第 2 条，改 Skill 要走六处同步（见 [C1](./C1-write-md-root-detection-is-wrong.md) 的"坑"）。
