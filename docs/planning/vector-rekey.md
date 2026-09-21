# 向量 rekey：卸下身份，再让排干重新上锁

状态：**已实现**（分支 `feature/vector-rekey`，等 CI 全绿后合回主线）。触发场景：用户前期没配
向量模型 → 后来配了想补齐；或者试用过一个小模型，把库的 `(model, dimensions)` 钉死了，之后换不动。

## 问题

每个 Database 一把 **TOFU 锁**：第一次 `ACCEPT VECTOR` 把 `(model, dimensions)` 写进
`mem_databases`，之后换模型被硬拒（`database X is locked to m/d vectors; m2/d2 cannot share
its index`），**没有任何解锁路径**。它很容易被无意触发——一次试用、或别的客户端用自己的默认
provider 抢先钉死，库就永久锁住了。

## 形状（已定：形式 B + 有界两阶段）

```sql
REKEY VECTOR IDENTITY IN DATABASE :db LIMIT :n [MODEL :m DIMENSIONS :d]
```

**一条语句、可重复、有界**，和 `REPAIR VECTOR INDEX` 同族（`LIMIT` 就是批大小，回执报
`released` / `remaining`）：

- **第一次调用**：校验 `expected_schema_version` 与当前锁状态 → 标记**中间态** → drop 该库全部
  vec0 虚表并删注册行（按表计，天然有界）→ 清掉至多 `LIMIT :n` 个单元的
  `embedding` / `embedded_content_hash` / `embedding_model` / `embedding_dimensions` /
  `embedded_at` → 回执 `remaining`；
- **后续调用**：继续清字节，直到 `remaining = 0`；**重复同一条语句（带上目标）才是在继续**；
- **最后一次**（`remaining = 0`）：写锁（给了 `MODEL`/`DIMENSIONS` 写新身份，没给清成未锁）→
  清掉中间态 → 回执 `state = done`。
- **逃生口**：对**已开窗**的库再发一条**不带目标**的 `REKEY`，就是把窗口改瞄成「卸成未锁」——
  已经释放的单元保持释放，剩下的照常释放，结束时库是未锁的。没有这条，一个没人收尾的窗口会把
  库无声卡在拒绝态（顾问指出：窗口必须可见且可终止）。

**授权级别：整条语句 L2。** 它 drop 虚表，是结构变更；也就是说这条语句自始至终是 L2，不随调用
次数变级（按状态变级无法验证）。真正的大批量工作仍在**排干**里——宿主嵌入 N 条 `ACCEPT VECTOR`
走现成的 **L1 有界**通道（`SHOW PENDING VECTORS … LIMIT 64` → 一个 request 装 N 条，CLI 每次
`exec` 最多 1024 个单元）。

**不选 A（一条语句做完 rekey）**：把「结构变更」和「全库清空」焊在一条语句里，`max_affected_rows`
对无界的单元数失去意义，违反有界写规矩。**也不做第二条语句**：中间态由同一条语句的第一次调用
建立、最后一次调用撤销，窗口天然被它包住。

## 中间态期间，向量层一律拒绝（这是 (b) 的代价，也是它的保障）

`mem_databases` 加**新列**（如 `embedding_rekey_at` + 目标 `model`/`dimensions`；**加列不 bump
`fileSchemaVersion`**），中间态置位期间：

- `RECALL … NEAREST` **拒绝**（稳定 code，倾向 `rekey_in_progress`）——**不返回空结果**，因为空
  结果和「没有命中」不可分辨；
- `ACCEPT VECTOR` **拒绝**——否则并发宿主会拿旧模型把 vec0 表按旧维度重建、把错误身份钉回锁上；
- `REPAIR VECTOR INDEX` **拒绝**——否则它会拿**旧模型字节**把新索引重建出来（见下面约束 ②）；
- `SHOW PENDING VECTORS` 带上**目标 `(model, dims)`**，宿主据此选模型；
- `doctor` 报中间态。

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

## 有界性（已定 (b)：两阶段有界）

`max_affected_rows` 对每个写都成立：`REKEY` 每次只动至多 `LIMIT :n` 个单元，`remaining` 递减到 0
才写锁、撤中间态。**不选 (a)**（一次做完、声明不受 `max_affected_rows` 约束）——那会让「每个写都
有界」这条不变量在最需要它的一次操作上失效。

## 实现要点（已落地）

- **守卫放在唯一的那次身份读取里**（`internal/sqlstore/embedding.go` 的 `vectorIdentity`）：
  accept、storeVector/写入时附带向量、recall、repair、doctor 全都经过它，所以拒绝天然被四条路
  继承，不需要在每个语句里各加一道（顾问指出：只在 executor/binder 加闸门绕不住库内路径）。
  原始读取另开 `vectorIdentityRow`，供**状态**与 **doctor** 使用——它们是窗口内唯二还能说话的地方。
- `mem_databases` 加三列（加列不 bump `fileSchemaVersion`）：`embedding_rekey_at` /
  `embedding_rekey_model` / `embedding_rekey_dimensions`。窗口**存下来**而不是推导，因为它正是
  「索引与身份故意不一致」的那一刻，崩溃丢了它就没人知道要重建。
- `dropVectorIndexes` 先删注册行再 DROP 虚表；`storeVector` 遇到注册维度与当前身份不符时**丢弃
  重建**，不再信任注册行（`float[N]` 焊死，这是一颗静默地雷）。
- 关键词召回窗口内照常作答，并带 `vectors_not_ready` 通知；`doctor` 报 `rekeying_databases`，
  且窗口内**不计** `vector_index_drift`（窗口不是损坏）。
- 未锁的库 + 不给目标 → 回执全零（没有身份可卸、没有派生层可丢）。

## 验收证据（TDD，全部已绿）

| 计划里的证据 | 落在哪 |
|---|---|
| RED：锁定后换模型被拒 → rekey 后可换 | `TestRekeyMovesTheIdentityInBoundedPasses` |
| 分次释放、回执 `released`/`remaining`/`rekeying` | 同上 + `TestRekeyStatementIsStructuralAndBounded` |
| 换维度必须 drop + 按新维度重建 | 同上（断言 `float[3]` → 空 → `float[4]` 与注册行） |
| 窗口内 `NEAREST` / `ACCEPT` / `REPAIR` / `PENDING` 一律拒 | 同上（断 `rekey_in_progress`） |
| 无目标 → 卸成未锁，下一次 `ACCEPT` 重新锁 | `TestRekeyWithoutATargetLeavesTheDatabaseUnlocked` |
| 中断/重开：窗口跨进程存活并能收尾 | `TestRekeyWindowSurvivesAReopen` |
| 对账不倒退（窗口内 `REPAIR` 拒绝，旧字节不会被写回） | 同上 + 守卫测试 |
| L2 / 有界 / 半截目标被拒 | `TestRekeyStatementIsStructuralAndBounded` |
| 关键词召回与 `doctor` 在窗口内仍可用 | `TestRekeyWindowLeavesTheRestOfTheInstanceUsable` |
| 每个 statement kind 都有样品、只读传输按分类拒绝 | `internal/devgate/statements_test.go` |

## 待定

- 并发两个宿主同时打在窗口上：写入是串行的，第二条 `ACCEPT` 会被拒；**没有**做多进程实测。
- 窗口内 `SHOW PENDING VECTORS` 是拒绝（现状）还是带目标身份放行——现状是拒绝，目标身份由
  `doctor` 与 `VectorStatus` 给出。
- Admin 是否要露出窗口（要动冻结 bundle）。

