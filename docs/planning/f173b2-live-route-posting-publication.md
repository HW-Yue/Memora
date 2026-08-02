# F173b2：Live Route Posting Publication

规划状态：已完成；2026-08-03 已通过 TDD、完整 CI 与完成门。

## 唯一主要结果

所有正式 native Route mutation 都在 body commit 后、返回成功前把完整 Route revision 原子发布到
Fulltext Tree；失败会 poison，reopen 从 native truth 收敛，不存在绕过发布协议的产品写路径。

## 写路径审计

当前有三条独立调用路径，但共享同一个物理结果：

1. `nativerow.Service` 的 root/node create、rename、synopsis update、delete；
2. `nativemutation.Coordinator.CommitRoutePlan` 的 create/move/delete batch；
3. `nativemutation.Coordinator.Commit` 的 Row+Relation+Route reshape。

三者都必须调用 Page Authority；Repository 的 `Stage*` 仍只负责 native transaction staging，不直接
依赖 Fulltext。测试/迁移用的裸 Repository 写入不承诺在线同步，由 F173b1 reopen reconciliation 收敛。

## 统一发布端口

`PageAuthority.PublishMutation(ctx, rows, routes, commit)` 是进程内统一端口：

```text
preflight current Catalog scope + Row/Route documents
→ commit immutable native bodies/change envelope
→ append Row Version（有 Row 时）
→ one Fulltext ReplaceBatch（Row + Route）
→ advance Current Row（有 Row 时）
→ success
```

`PublishRows` 保留为只含 Row 的薄包装，既有调用协议和故障点不变。route-only 不触碰 Row trees；
combined reshape 的 Row 和 Route documents 必须进入同一个 Fulltext root revision，不能拆成两次提交。

Authority 已有 operation gate 由上层持有，发布端口只取得内部 publication mutex，不能递归获取 gate。

## Route change document

- live Node 使用 F173b1 的完整 Route surface；deleted Node 在自身当前 revision 写零 field tombstone；
- batch 必须按 Route identity 确定，重复 object、scope drift、revision gap 由 Fulltext Store 稳定拒绝；
- projection 和 Catalog Table scope 校验先于 body commit，失败不 poison、不写 native body；
- membership-only mutation 不改变 Route semantic surface，不产生 posting document。

## Rename subtree

Route path 是 lexical surface。重命名 Branch 时，所有 live 后代 path 必须在同一 native transaction 更新，
每个受影响 Node revision 前进一，并在同一个 Fulltext batch 替换；原 name 按现有 Router alias 语义保留，
但后代旧 path 词项必须消失。Leaf rename 退化为单 Node batch。

## 故障与恢复

Route 增加 `body-committed`、`fulltext-published` checkpoint。body 后任意失败返回 outcome unknown 并
poison；Authority 在 reopen 时由 F173b1 Plan v3 收敛，revision gap 必要时 COW。真实 Fulltext WAL
失败也必须证明旧 root 可读、进程拒绝继续服务且重开得到新 Route。

## Failure matrix

| 证据 | 故障/边界 | 稳定结果 |
| --- | --- | --- |
| projection | live/delete、非法 scope/revision、duplicate batch | commit 前拒绝 |
| direct CRUD | root/node create、rename、synopsis、delete | 最新 posting/tombstone |
| rename subtree | Branch + descendants | 同批 revision，后代旧 path 为零 |
| Route Plan | create/move/delete batch | 一次 Fulltext root revision |
| reshape | Row + Route | 同一个 Fulltext root revision，二者都可查 |
| checkpoint | Route body/Fulltext | poison；reopen 收敛 |
| WAL fault | Fulltext WAL commit | outcome unknown；旧 root 不混合；reopen 收敛 |
| replay | reopen 已一致 | 不追加 Fulltext WAL |
| race | Route read/write、Row+Route writers | 无 data race，只观察完整 revision |

## 产品门审计

- 上下文预算增加 0；不新增 SQL、模型、Provider、Agent、向量、chunk 或答案入口；
- Fulltext 仍是派生位置，native Route 是 authority，事实读取仍须 SQL 回表；
- membership 不进入 document，一个 Leaf 最多一个活跃 Row 的不变量不变；
- 显式 rebuild 与用户可见 lexical query 留给 F173c、F174；
- 用户连续执行授权覆盖本项，不扩张相邻 Feature 的主要结果。

## 完成证据

- direct root/node create、Branch rename + descendant path revision、synopsis update 与 delete 均发布最新 posting/tombstone；
- Route Plan 的 move 与同批 create+delete、Row+Route reshape 都只推进一次 Fulltext root revision；
- 非法 Catalog scope 在 body commit 前拒绝；Route checkpoint 与真实 Fulltext WAL 故障均 poison，reopen 收敛；
- 已一致 Route 重开不追加 Fulltext WAL；`./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e
  以及独立 cross-build 全部通过。

完成门结论：PASS。F173c 承担显式全量 rebuild 与 snapshot parity，F174 承担用户可见查询。

## 关联

- [F173b1](./f173b1-route-posting-generation.md)
- [Page Store Authority](../storage/page-store-authority-v1.md)
- [TDD 协议](./feature-tdd-protocol.md)
