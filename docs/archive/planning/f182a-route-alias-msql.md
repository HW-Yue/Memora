# F182a：Route Alias MSQL Round-trip

状态：已完成；单项 Review、RED → GREEN → REFACTOR 与全部完成门均已通过。

## 唯一主要结果

让结构权限调用方通过版本化 MSQL 原子替换一个 Route 的有界 alias 集合，并从 Route
读取结果中无损读回。它解除 F183 只经 MSQL 物化 F182 fixture 的缺口，不引入 Agent
旁路，也不实现 answer runner。

## 用户故事与标准旅程

- `US-F182A-01`：评测物化器可保存 fixture 中的中英文 Route aliases，后续 lexical posting
  与 Query Agent 看到的语义面不丢失；
- `US-F182A-02`：并发 Agent 基于旧 revision 写 alias 时得到稳定冲突，不覆盖新结果；
- `US-F182A-03`：AI 可用相同 MSQL 验证实际 alias，不读取 Router/Store 内部对象。

```sql
ALTER ROUTE :route_id SET ALIASES :aliases;
DESCRIBE ROUTE :route_id;
SHOW ROUTES UNDER :parent_id LIMIT 12;
```

`:aliases` 必须是 TEXT 数组；`[]` 明确清空。每次最多 8 个，每项去除首尾空白后为
1–64 个 Unicode 字符，整个规范化数组最多 512 UTF-8 bytes；alias 不得与当前 name
或其他 alias 大小写不敏感地重复。顺序保持不变，不由引擎排序或推断。

## 事务、读取与边界

- mutation 必须携带 `expected_revision`、结构权限、provenance 与正常 affected-row 上限；
- 成功只生成一个新 Route revision，并由现有 Route publication 在同一提交中替换 lexical
  document、记录 Change；失败或 publication fault 不暴露半更新；
- `DESCRIBE ROUTE`、`SHOW ROUTES` 与 Route mutation receipt 增加非 null `TEXT_LIST aliases`；
  `SHOW` 仍不返回 synopsis，alias 上限保证每层 Route Frame 有界；
- name、path、kind、purpose、synopsis、membership、Row revision 与历史语义不变；
- 不在本 Feature 增加 create-time alias、增量 add/drop alias、Route Plan alias 编辑或 F183 runner。

## RED 与验收

- parser/AST：`ALTER ROUTE :route SET ALIASES :aliases` 当前无法解析；
- executor contract：合法中英 alias round-trip，空数组清空，错误类型/空白/重复/同名/超限
  fail closed，旧 revision 冲突；
- native integration：写入后 reopen 仍一致，Change revision 与 live lexical alias posting 同步；
- publication fault：body 已提交后的注入故障返回 outcome unknown 并 poison authority；reopen 必须
  从 Route 权威 revision 收敛 alias 与 posting，不允许永久半更新；
- `internal/agent` import allowlist 不变，F183 只能使用版本化 MSQL。

用户执行授权：2026-08-03 用户要求继续顺序执行后续 Feature；实现调查发现 F183 fixture
alias 无法由当前 MSQL 表达，因此先独立补齐本前置能力。

开工前结论：PASS。

## 完成证据

- parser/AST 已区分 `SET SYNOPSIS` 与 `SET ALIASES`，缺参数和未知属性 fail closed；
- executor 同时接受协议解码后的 `[]any` 与同进程 `[]string`，错误 shape、空白、同名、重复、
  数量/字符/byte 超限均返回 `validation_error`，`[]` 返回非 null 空列表；
- embedded transaction 的 alias mutation 可完整 rollback；旧 expected revision 返回
  `revision_conflict`，不会覆盖现值；rename 也遵守相同上限并移除成为当前 name 的旧 alias；
- native authority 测试证明 replacement 删除旧 posting、发布中英文新 posting，健康 reopen 后
  `DESCRIBE ROUTE` 仍返回 revision 3；body-commit 后 fault 会 poison，reopen 后 Route 与 posting
  收敛到同一 revision；
- `SHOW ROUTES`、`DESCRIBE ROUTE` 与 mutation receipt 均返回非 null `TEXT_LIST aliases`，不增加
  synopsis、Row 正文或引擎内部字段；
- `GOCACHE=/private/tmp/memora-go-cache ./scripts/ci.sh` 的 format、vet、unit、race、integration、
  e2e 全绿；独立 cross-build 也全绿。

旁路审计：`internal/agent` 未新增任何 import 或内部调用；F183 物化器可且必须只提交 MSQL。

完成门结论：PASS。下一项是 F183 端到端 answer runner。
