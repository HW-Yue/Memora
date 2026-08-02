# F172b：Live Row Posting Publication

规划状态：已完成；2026-08-03 Review、RED → GREEN → REFACTOR、验收与完整 CI 通过。

## 唯一主要结果

每次由 Page Authority 接受的 Row INSERT/UPDATE/DELETE/supersede，都在返回成功前把当前 revision
原子替换进 Fulltext Tree；正文提交后的任意中断通过 poison + reopen 收敛，不留下陈旧 posting。

## 用户故事与标准旅程

- `US-F172B-1`：Agent 用现有 MSQL 修改 Row 后，未来 lexical location 不会命中旧正文；
- `US-F172B-2`：写入中断后，用户重启 Memora 即得到正文、Version、Current、Fulltext 一致状态；
- `US-F172B-3`：split/merge 等同一 native transaction 的多 Row revision 在 Fulltext Tree 内一次提交。

标准 MSQL 不新增语法：

```sql
INSERT INTO work.notes (title) VALUES ('old term');
UPDATE work.notes SET title = 'new term' WHERE row_id = :row_id EXPECT REVISION 1;
DELETE FROM work.notes WHERE row_id = :row_id EXPECT REVISION 2;
```

F172b 只验收内部 posting 状态；有界查询入口由 F174 提供，不能提前宣称用户已能全文搜索。

## 发布协议

在持有既有 Authority publication barrier 时：

1. 用当前 Catalog 预投影全部 Row document；投影失败时正文尚未提交；
2. 提交 immutable Row/history/change/membership body；
3. 一个 Version Tree transaction append 全部 locator；
4. 一个 Fulltext Tree transaction replacement 全部 Row document；
5. 一个 Current Tree transaction advance 全部 locator；
6. 全部成功后才释放 barrier 并向调用方返回成功。

Fulltext 增加 batch replacement，但复用 F171 object/owner/posting、revision 和单 MutationPlan/WAL commit；
不是新的跨树事务。Version → Fulltext → Current 的顺序让 Current 保持最终可见指针。任一正文提交后
失败都返回 `ErrOutcomeUnknown`、poison 当前 Authority，并拒绝继续服务。

## Reopen reconciliation

F172b 完成时启动以 native body 构建 Plan v2；当前已由 F173b1 的 Plan v3 取代，收敛顺序仍为
Catalog、Version、Fulltext、Current。Fulltext 当前 revision
与 body 连续时幂等 replacement；相同 revision/same digest 为 replay。

若绕过 Authority 的旧写入造成 revision gap，不允许 Fulltext 跳号覆盖，因为这会掩盖漏删旧 posting；
启动转为现有 COW generation replacement，以当前 Plan body 重建 v2，durable marker 切换后才服务。
codec/Page/WAL corruption 继续 fail closed，不能用 rebuild 偷渡损坏。

## Failure matrix

| 证据 | 故障点 | 稳定结果 |
| --- | --- | --- |
| preflight | schema/type/projection 无效 | commit 未调用，Authority 仍健康 |
| live revision | insert/update/delete/supersede | 旧 term 消失，新 term/revision 或 tombstone 生效 |
| batch | 两个以上 Row、重复 object | 单 Fulltext root commit或稳定拒绝，绝无半 batch |
| publication | body/version/fulltext/current 后 checkpoint | outcome unknown + poison；reopen 与 body reference 一致 |
| WAL fault | Fulltext commit 失败 | 旧 root 完整；进程 poison；reopen 重放/收敛 |
| gap | Fulltext N、body N+2 | COW v2 rebuild，不直接跳 revision |
| race | 多 reader/同 Row writers | writer one-winner；读者只在 barrier 外观察完整状态 |

## 产品门审计

- 作用域是 Row 派生索引 publication，不改变 Route、Schema、History 或 MSQL 协议；
- 不增加模型调用、上下文、向量、chunk、Provider 或 Agent 旁路；
- Fulltext 只保存 lexical 位置，事实仍必须由未来 F174 返回 RowID 后 SQL 回表；
- `US-F172B-*` 不需要新的 prompt/Route Frame，因而上下文预算增加为 0；
- F173a 的 Catalog/Route document、F173b rebuild 命令、F174 查询均明确不做。

用户执行授权：2026-08-03 用户明确要求顺序执行完后续 Feature；本次授权仅落实上述 F172b 范围。

## RED 与完成门

RED 入口：

```text
go test ./internal/store/fulltextindex ./internal/pagestoremigration
```

完成时执行 batch reference、publication fault/reopen、revision-gap COW、受影响包 race及完整 CI。

开工前结论：PASS。

## 完成证据

- RED `b73e6e8` 证明 batch、在线 posting、Fulltext checkpoint 与 revision-gap COW 均缺失；
- INSERT/UPDATE/DELETE 后 old term 消失，posting revision 与当前 Row 一致；
- 两 Row reshape 经真实 `nativemutation.Coordinator` 只推进一次 Fulltext Tree revision；
- batch 重复 object、跳 revision 和关闭 WAL 均在旧 root 完整可读时稳定拒绝；
- body/version/fulltext/current 四个 checkpoint 及真实 Fulltext WAL 故障全部 poison，reopen 与 body 收敛；
- 相同 revision reopen 为零 WAL replay，N → N+2 走 epoch+1 COW 并保留旧 generation；
- 预投影错误不会调用 body commit，也不会 poison Authority；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e 与双架构 cross-build 全绿。

`US-F172B-1`、`US-F172B-2`、`US-F172B-3` 均通过。无新 Agent/SQL/向量/正文旁路；
用户可见 lexical location 仍等待 F174。

完成后结论：PASS。

## 关联

- [F172a](./f172a-row-posting-generation.md)
- [F171](./f171-persistent-posting-store.md)
- [Page Store Authority](../storage/page-store-authority-v1.md)
- [TDD 协议](./feature-tdd-protocol.md)
