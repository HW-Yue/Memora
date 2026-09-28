# C2 · `memora mutate` 不排干向量，而 `write.md` 让你优先用 `mutate`

严重度：high（静默的派生层缺口：写全成功，向量永远缺）。状态：**已关**（2026-09-26 复核，同日修复）。

## 结案

选了首选修法：**让 `mutate` 也排干**，这样「推荐路径」与「自动排干」重合，Skill 的推荐不再需要
附加条件。若只改 Skill 文本，坑还在——agent 照推荐走，向量照样缺。

- `runMutate` 在回执写出后调用与 `exec` 同一个 `drainAfterWrite`；授权取 **plan 自己的**
  `authorized_databases`（与 `skillwrite` 给 plan 语句的授权同形），排干不会越过 plan 的范围。
- 它跑在 `committed_unverified` 的退出判断**之前**：排干没跑完成只让单元保持未就绪，
  不改变「写已提交」这件事的退出语义。
- 证据：`fix(cli)` 分支 → merge `dfffc398`；测试 `TestMutateDrainsPendingVectorsLikeExec`
  （进程内真 daemon + stub 往返 + 真 OpenAI 兼容 provider，改之前断言「没有任何排干请求」失败）、
  `TestMutateWithoutAProviderDoesNotDrain`。
- Skill 两处措辞跟着改（`exec` 与 `mutate` 都排干；plan 在自己授权范围内排干），
  已按六处同步流程同步并发到发布仓库。

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
