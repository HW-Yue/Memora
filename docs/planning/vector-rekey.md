# 向量 rekey：卸下身份，再让排干重新上锁

状态：**实现计划（未授权开工）**。触发场景：用户前期没配向量模型 → 后来配了想补齐；
或者试用过一个小模型，把库的 `(model, dimensions)` 钉死了，之后换不动。

## 问题

每个 Database 一把 **TOFU 锁**：第一次 `ACCEPT VECTOR` 把 `(model, dimensions)` 写进
`mem_databases`，之后换模型被硬拒（`database X is locked to m/d vectors; m2/d2 cannot share
its index`），**没有任何解锁路径**。它很容易被无意触发——一次试用、或别的客户端用自己的默认
provider 抢先钉死，库就永久锁住了。

## 形状（顾问结论：B，把「重新上锁」显式化）

```sql
RELEASE VECTOR IDENTITY IN DATABASE :db [MODEL :m DIMENSIONS :d]
```

- **L2**（结构变更：它要 drop 虚表），同一事务里按顺序做：
  1. 校验 `expected_schema_version` 与当前锁状态；
  2. **drop 掉该库每一张 vec0 虚表并删掉它的注册行**（`mem_recall_vec_<tableID>` +
     `mem_recall_vec_indexes`）；
  3. **清空该库每个单元的** `embedding` / `embedded_content_hash` / `embedding_model` /
     `embedding_dimensions` / `embedded_at`；
  4. 写锁：给了 `MODEL`/`DIMENSIONS` 就写新身份（形式 C），没给就清成「未锁」（形式 B）。
- 之后的**重新上锁由排干循环里第一条 `ACCEPT VECTOR` 完成**（现成的 TOFU），排干走现成的
  **L1 有界**通道：`SHOW PENDING VECTORS … LIMIT 64` → 宿主嵌入 → 一个 request 装 N 条
  `ACCEPT VECTOR`，CLI 每次 `exec` 最多排 1024 个单元。

**不选 A（一条语句做完 rekey）**：把「结构变更」和「全库清空」焊在一条语句里，`max_affected_rows`
对无界的单元数失去意义，违反有界写规矩。

## 两条必须写进实现的约束（否则会静默出错）

1. **RELEASE 必须自己把派生层清干净，不能指望事后对账。**
   `repairVectorIndex` 在锁为空时**直接早退**（"No vector was ever accepted"），所以「先清锁、
   再让 repair 收尾」会让 vec0 虚表和注册行永远留着。
2. **清 `embedding` 字节不是可选项，而且必须在锁翻转之前/同事务完成。**
   `tableVectorDrift` 取真相时**不看 model**（只 `embedding IS NOT NULL`），所以索引表一旦被
   drop，一次 `REPAIR VECTOR INDEX` 会**拿旧模型的字节把新索引重建出来**；维度还焊在
   `float[N]` 里，`storeVector` 又不比对维度（只看「注册过且物理存在」）。旧字节不先清掉，
   中间任何一次对账都会把错的东西写回派生层。

## 最大的风险：静默空窗

RELEASE 与排干完成之间，库**可检索但向量召回为空**——不是报错，是悄悄退化成零结果；而且锁是
敞开的，**任何并发宿主的第一条 `ACCEPT VECTOR` 都能把错误的 `(model, dims)` 钉死**。

**缓解（本计划要求）**：在 `mem_databases` 上加一个显式**中间态**（如 `embedding_rekey_at` /
`embedding_state`）：

- 中间态期间 `RECALL … NEAREST` **拒绝**（稳定的 code + notice），而不是返回空结果；
- `SHOW PENDING VECTORS` 在中间态带上**目标 `(model, dims)`**，宿主才知道该拿哪个模型嵌入；
- 第一条 `ACCEPT VECTOR` 成功即退出中间态（重新上锁）；给了 `MODEL`/`DIMENSIONS` 的形式在
  RELEASE 那一刻就写锁，中间态只到排干完成为止；
- `doctor` 报这个中间态。

## 有界性怎么处理（本计划里唯一还需要定的）

清 `embedding` 的字节数**无界**（单元数），而 `max_affected_rows` 是有界写的前提。两条候选：

- **(a) 一次做完**：RELEASE 明确声明「这是结构级全库操作，不受 `max_affected_rows` 约束」，
  借口是它已经在 L2 且中间态挡住了读；
- **(b) 两阶段有界**：RELEASE 只做「标记中间态 + drop 派生层 + 写新锁」，清字节另开一条带
  `LIMIT :n` / `remaining` 的有界语句反复跑到 0，完成后再允许排干。

倾向 (b)：它保住「每个写都是有界的」这条不变量，代价是多一条语句和一次状态检查。

## 要动的地方

- `internal/sqlstore/vecindex.go`：新增 release 路径（drop 虚表 + 删注册行 + 清字节 + 写锁），
  并让 `vecIndexReady` / `storeVector` 在维度与当前身份不符时**不信任**旧表；
- `internal/sqlstore/db.go`：`mem_databases` 加中间态列（加列不 bump `fileSchemaVersion`）；
- `internal/msql/{ast,parser,binder,executor}`：新语句 + L2 授权 + 参数校验；
- `internal/sqlstore/embedding.go`：`vectorStatus` / `pendingVectors` 带上目标身份；
- Skill：`docs/query/msql.md` 与 `skills/memora/SKILL.md` 增加 rekey 一节（改了 skill 要跑
  `scripts/sync-skill.sh --check`）；
- `internal/adminui`：只在确认要在 Admin 露出中间态时才动（冻结 bundle，要同步哈希）。

## 验收证据（TDD）

1. RED：锁定一个库并嵌入若干单元 → 用另一个模型 `ACCEPT` 被拒（现状），rekey 后同一条被接受；
2. 真机旅程：同维度换模型（不动 vec0 表）与换维度（**必须 drop + 按新维度重建**）各跑一遍；
3. 中间态：RELEASE 后 `NEAREST` 被拒而不是空结果；`SHOW PENDING VECTORS` 带目标身份；
4. 中断/崩溃：RELEASE 后、排干中途 kill daemon → reopen 后仍在中间态，且能继续排干；
5. 并发：两个宿主同时对中间态库 `ACCEPT` → 不会钉死错误身份（第二条被拒或按目标身份拒绝）；
6. 对账不倒退：RELEASE 与排干之间跑 `REPAIR VECTOR INDEX` → **不会**把旧模型字节写回；
7. reopen / `doctor` / `vector_index_drift = 0` / `./scripts/ci.sh` 全绿。

## 待定

- 有界性选 (a) 还是 (b)。
- 中间态是 `mem_databases` 上的新列，还是复用 `embedding_locked_at` 的语义（倾向新列，语义不同）。
- `RECALL … NEAREST` 在中间态是稳定拒绝还是返回带 notice 的空结果（倾向拒绝：空结果不可分辨）。
