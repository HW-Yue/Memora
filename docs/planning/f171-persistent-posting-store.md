# F171：持久化 Posting Store

规划状态：已完成；2026-08-03 单项 Review、RED → GREEN → REFACTOR 与完整 CI 均通过。

## 唯一主要结果

在现有 Page、B+ Tree、WAL 和 `treecommit.Runtime` 上提供一个可重开的全内容 posting store。
它持久化 F170 canonical document 的当前 revision，并原子替换该对象拥有的全部 postings。

F171 不接入 Row/Catalog/Route 写事务，不增加 MSQL，不实现 generation 发布器，也不改变
Page/WAL 算法。它只是 F172–F174 可复用的派生索引物理层。

## 输入与编译边界

- store 只接收 `fulltext.Document`，并调用 F170 的公开 `Compile` 入口；
- `Compile` 输出规范化身份、state、revision、digest 和排序 postings；
- UTF-8、identity、完整 snapshot、词项和 revision 规则仍由 F170 冻结；
- 单 term UTF-8 编码最多 2048 bytes，使一条 B+ Tree entry 始终可落入普通 Page；
- 不持久化原值、文档 chunk、snippet、Embedding 或答案。

## 单树 key space

一个 generation 使用一个独立 `treecommit.Runtime`。key version 为 1，三类记录共存于同一 B+ Tree：

```text
object:  kind + object_id
      → database_id + table_id + revision + state + digest

owner:   kind + object_id + term + field_id
      → revision + frequency

posting: term + database_id + table_id + kind + object_id + field_id
      → revision + frequency
```

`object` 保证相同 kind/object_id 不可跨 scope 漂移；`owner` 允许 replacement 精确枚举旧词；
`posting` 支持 term 后按已授权 Database/Table 前缀读取。所有整数采用固定字节序，字符串使用
长度前缀；decoder 必须拒绝非 canonical、未知版本、非法 UTF-8、保留位和 key/value 身份不一致。

## 写入语义

`Bootstrap(transactionID, documents)` 只允许空树，从任意正 revision 的完整当前 snapshot 建树。

`Replace(transactionID, document)`：

1. 锁定 index 并读取 object 与全部 owner；
2. 校验首次 revision 1，或严格 N → N+1；
3. 相同 revision + digest 返回 replay，不写 WAL；
4. 在一个 `btree.MutationPlan` 中删除旧 owner/posting、写入新 owner/posting、更新 object；
5. 用一次 `Runtime.Commit` 发布；失败时不更新可读 root。

deleted/superseded document 保留 object tombstone，但没有 owner/posting。receipt 的 Added/Removed
按 F170 posting value 比较；revision 变化会让仍存在的词也各计一次 remove/add。

## 读取与一致性

- `Postings(term)` 返回稳定排序的当前位置，不返回原值；
- `AllPostings()` 仅供 rebuild 校验和测试，不进入 Agent/MSQL；
- 读取 posting 时交叉检查对应 object、owner、scope、revision 和 state；
- orphan、缺失镜像或不一致必须返回 `ErrCorrupt`，禁止扫描 Row 正文回退；
- 空树返回空结果；单 term 读取在 F174 以前暂不承诺公开预算/cursor。

## Failure matrix

| 证据 | 故障点 | 稳定结果 |
| --- | --- | --- |
| codec corpus | header/version/length/UTF-8/reserved | `ErrCorrupt` |
| split/reference | 大量 documents/postings | internal root；结果与 F170 对拍 |
| crash reopen | WAL durable、Page 未 flush | recovery 后字节级等价 |
| Page bit flip | 已 flush leaf/root | 读取 `ErrCorrupt`，无 fallback |
| commit fault | WAL 在 commit 前不可用 | replacement 失败，旧 root 仍可读 |
| concurrent readers | writer 连续 replacement | 只观察完整 revision；`-race` 通过 |

F171 复用已完成的 WAL fsync/truncate/fault matrix 和 B+ Tree split/rebalance 证明，不重复实现其算法；
本 Feature 必须证明自己的编码、mutation 组合和 reopen 行为真实经过这些组件。

## RED 与完成门

RED 入口：

```text
go test ./internal/store/fulltextindex
```

首批测试至少覆盖 bootstrap/reopen、revision replacement、delete/supersede、split/reference、
corruption corpus、commit fault 和 reader/writer race。完成时执行受影响包 race 与 `./scripts/ci.sh`。

## 产品门与永久边界

- 用户故事：断电重开后关键词位置仍可恢复，修改后旧词不再命中；
- 最终事实必须 SQL 回表；store 不返回正文或答案；
- F171 不形成第二套生产查询入口；损坏时 lexical unavailable，Router/SQL 权威不变；
- Agent 永远不能 import 或调用本 package；F174 只能经 MSQL 暴露有界位置；
- 可独立回滚：删除该派生 store 不损坏 Catalog、Row、Route 或 History。

用户执行授权：2026-08-03 用户要求顺序完成后续 Feature；本 Review 只批准 F171 上述范围。

开工前结论：PASS。

## 完成证据

- 500 对象建树触发 internal root，结果与 F170 reference index 完全一致；
- 120 步固定 seed revision 序列、乱序输入 Page bytes、replay 和 tombstone 均通过；
- durable WAL/unflushed Page reopen、Page bit flip、closed-WAL commit fault 均得到预期状态；
- codec corruption corpus 和 object/owner/posting 全镜像校验通过；
- `go test -race ./internal/store/fulltextindex` 与 `./scripts/ci.sh`：PASS。

完成门结论：PASS。生产 Row 发布仍由 F172 接入。

## 关联

- [F170 reference index](./f170-inverted-index-surface.md)
- [ADR-0008](../decisions/0008-full-content-inverted-index.md)
- [持久化索引](../storage/indexing.md)
- [TDD 协议](./feature-tdd-protocol.md)
