# F176：确定性 Query Bootstrap Frame

规划状态：已完成；RED → GREEN → REFACTOR 与完整 CI 通过。

## 唯一主要结果

在 `internal/agent` 建立无模型、只经 MSQL 的 Query Bootstrap assembler：输出完整或显式部分覆盖的
Catalog Atlas、当前问题的 lexical locations，以及最多两张候选 Table 的根 Route 投机预取。

F176 不调用 Provider、不选择最终 Table/Route、不读取 Row 正文、不生成答案，也不接 daemon/CLI。

## 确定性调用顺序

1. 第一条 MSQL Request 用两个 statement 同时取得 Atlas 首页与 lexical locations；
2. Atlas 有 cursor 时按同 snapshot 继续分页，直到 complete、页数门或 Frame byte 门；
3. 按 lexical rows 已有顺序取唯一 `(database_id, table_id)`，只对 Atlas 已解析出名字的前两张
   Table 组成一个 root Route batch；标识符使用 MSQL quoted identifier；
4. root 失败或超 Frame 预算只记录 unavailable/budget-skipped 收据，未来选择该 Table 时走普通 fallback。

不得因 lexical 零命中、Atlas partial 或 root 预取失败排除任何授权 Table。

## Frame 与预算

Frame 版本为 `memora.query-bootstrap-frame/v1`，`usage` 固定 `navigation_only`，包含：

- Catalog entries、snapshot、pages、complete、next cursor；
- lexical locations、snapshot、truncated、next cursor；
- 每个 root prefetch 的 Table identity、status、routes、snapshot 与 continuation；
- 实际紧凑 JSON UTF-8 bytes 和顶层 truncated。

Atlas、lexical 和各 root 的 snapshot 独立保留；assembler 只校验同一 Atlas continuation 的 snapshot，
不把多个派生视图合成一个伪 snapshot。最终 `json.Marshal(Frame)` 不得超过全局 byte limit。

默认预算：Atlas 每页 64 entries/8192 bytes、最多 8 页；lexical 16 locations/2048 bytes；
最多 2 个 root、每个 12 Route；Frame 12000 UTF-8 bytes。调用方可在 MSQL 合法范围内收紧。

## 失败边界

- Request/budget、初始 Atlas/lexical result、Atlas continuation 或 snapshot 不合法：整个 Assemble 明确失败；
- root result 失败：Frame 仍成功，并保留稳定错误码与 fallback 状态；
- 初始导航内容自身装不进 Frame：明确 byte-budget 错误，不静默丢 Atlas/lexical；
- assembler 校验每个 outbound Request 和 inbound Envelope，不重试 executor/context error。

## 完成证据

- scripted fake 精确验证 initial/continuation/root MSQL transcript；
- Atlas complete/partial、lexical zero-hit、root hit/fallback/unavailable；
- continuation snapshot mismatch、malformed result、context cancellation；
- 全局 JSON byte budget 与 root budget-skipped；
- Agent import allowlist、unit/race/vet 和完整 CI 全绿，测试不打开 Instance。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F176 范围。

开工前结论：PASS。

## 完成结论

- `BootstrapAssembler` 已用首批双 statement、Atlas continuation 和 root batch 组成确定性调用序列；
- Frame 保留 Atlas、lexical、各 root 的独立 snapshot，Atlas 页间漂移明确失败；
- lexical zero-hit、Atlas partial、root statement/可选 executor 失败均保持正常 fallback；
- 最终紧凑 JSON byte receipt 自洽且不越总预算，过大的 root 被替换为 `budget_skipped` 收据；
- scripted fake transcript 与真实 native daemon + SDK adapter 垂直链均通过；Agent 测试未打开 Instance；
- format/vet/unit/race/integration/e2e 与独立 cross-build 全绿。

完成结论：PASS。下一项为 F177 Memora-owned Provider port。
