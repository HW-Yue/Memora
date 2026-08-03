# F182a：Route Alias MSQL Round-trip

状态：已批准；正在执行 RED → GREEN → REFACTOR。

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
- publication fault：注入提交失败后 Route revision、alias 和 posting 都保持旧值；
- `internal/agent` import allowlist 不变，F183 只能使用版本化 MSQL。

用户执行授权：2026-08-03 用户要求继续顺序执行后续 Feature；实现调查发现 F183 fixture
alias 无法由当前 MSQL 表达，因此先独立补齐本前置能力。

开工前结论：PASS。

