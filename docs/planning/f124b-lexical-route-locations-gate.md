# F124b Lexical Route Locations 开工与完成门

状态：已完成。

## 单一主要结果

通过参数化 MSQL 对当前授权的 Database/Table/Route 语义表面做确定性字面位置聚合，
以 F124a Discovery Frame 返回有界、带来源的导航候选。

## 产品门

- 用户故事：第一次发现即可得到“信息大概在哪”的廉价提示，同时仍能选择零命中 Table。
- AI-native：AI 维护和读取的 Router 仍是权威；字面候选只减少一次续推机会。
- 隐私：索引输入与输出均不含 Row、History、membership、snippet 或答案。
- 回退：零命中与候选截断均成功，普通 Catalog/Router 全部保持可用。

结论：PASS。

## RED 证据计划

入口：`go test ./internal/routelexical ./internal/msql/parser ./internal/msql/executor`

1. `TestSearchAggregatesOnlySemanticLocationFieldsAcrossLanguages`：中英文 query 返回稳定位置、字段和计数。
2. `TestSearchUsesLatestLiveRouteAndNeverAcceptsRowsOrMemberships`：旧/deleted revision 不参与，输入类型无事实载荷。
3. `TestSearchSnapshotIsDeterministicAndChangesWithVisibleMetadata`：排序无关、当前 surface 改变即换快照。
4. `TestSearchRejectsInvalidQueryAndCorruptScope`：空/过长/超词项和跨 Table Route 稳定失败。
5. `TestParseLexicalRouteCandidatesRequiresAllBudgets`：语法、参数顺序和缺失 clause 冻结。
6. `TestLexicalRouteCandidatesEnforcesAuthorizationBudgetAndZeroHitFallback`：真实 executor 过滤权限、传播截断且零命中成功。
7. native 与 legacy Route source 测试：只列当前 node，reopen 后一致。

RED 已确认：首次运行时 `internal/routelexical` 无生产代码，Parser 以
`expected TRACE or TRACES` 拒绝新语法；实现后同一测试转绿。

## 明确不做

- 不搜索 Row/Column value，不返回 snippet；
- 不创建持久化 posting/generation 或后台维护链路；
- 不实现向量、HNSW、融合、自动选表或 Skill 预取；
- 不改变 Route 正常导航与 SQL 回表。

## 完成门

- targeted 与 race 测试通过；
- `./scripts/ci.sh` 全门通过；
- parser/executor、权限、预算、current revision、reopen、确定性和中英文 fixture 有证据；
- 文档明确零命中不是排除信号，候选不能作答。

## 完成证据

- targeted、受影响 package race 与 vet：通过；
- `./scripts/ci.sh`：format、vet、unit、全量 race、integration、e2e、cross-build 全通过；
- 英文/中文 bigram、字段计数、稳定排序、授权过滤、候选/字节截断、零命中、当前
  Route revision、native reopen 和 Batch Result Envelope 均有测试；
- `routelexical.Source` 只接受 Catalog 与 Route node，生产读取不访问 membership/Row；
- 实现为查询期短生命周期 posting map，未增加持久化格式和后台维护；
- 未覆盖项：F124c Route Vector Generation、F124d CPU exact、F124e Skill 组合。
