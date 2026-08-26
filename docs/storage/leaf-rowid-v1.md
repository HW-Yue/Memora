# 语义索引叶子直挂 RowID：membership 的废弃与迁移

状态：**迁移设计**（2026-08-22）。落实[写入形态](../product/write-model.md) §2
「叶子直接挂 RowID，不再有独立的 membership 对应关系」，不是独立规范——
与写入形态冲突时以写入形态为准。**尚未排期，未开始实现。**

编写原则同[存储层总览](./README.md)：每条「现状」断言都能指到具体文件与行，
指不到的不写。

## 一句话

`router.Node` 上没有能放 RowID 的字段，于是叶子到行的对应被做成了一个独立的、
自带版本号和墓碑的记录类型。**给叶子加上那个字段，这个记录类型整体退场**——
它的职责一部分被吸收进叶子，一部分改由 Row 上的一个默认字段承担，
还有一部分结构性地消失。

## 1. membership 是什么

```go
// internal/router/model.go:43
type Membership struct {
    LeafID             string  // 语义索引叶子
    MembershipRevision uint64  // 这条挂载关系自己的版本号
    Deleted            bool    // 墓碑，不是真删
    Locator                    // → {DatabaseID, TableID, RowID, Revision}
}
```

`router.Node`（`internal/router/model.go:20-34`）的字段是
`ID／DatabaseID／TableID／ParentID／Name／Aliases／Path／Kind／Purpose／Synopsis／
Revision／Deleted`——**没有任何一个能放 RowID**。所以查询走到叶子之后，
必须拿 `leaf_id` 再查一处才能拿到 RowID。

native 侧它是**两个** object kind（`internal/store/native/file.go:56-65`）：

| kind | 值 | 方向 | 键 |
|---|---|---|---|
| `ObjectKindRouteMembership` | 9 | 叶子 → 行 | `LeafID@RowID[@revision]` |
| `ObjectKindRouteRowMembership` | 13 | 行 → 叶子 | `RowID@LeafID[@revision]` |

第 13 号同时是 `ObjectKindMax`（同文件 65 行），而
「Discovery 与 migration sweep 枚举 `[ObjectKindDatabase, ObjectKindMax]`」——
删掉它会改变这个上界，迁移扫描要一并处理。

## 2. 现状：哪套实现在跑

生产 daemon 只构造 `nativerouter.New(nativeFile)`
（`internal/daemon/lifecycle.go:199`），喂给 `nativerow.NewService` 与
`nativemutation.NewService`。

`internal/router/service.go` 那套 bucket 实现（`router_leaf_members`／
`router_row_memberships`，文件 24-25 行）只能经 `row.New`
（`internal/row/service.go:73`）到达，而 **`row.New(` 的调用方全部是测试**。
按[旧代码清理边界](../development/legacy-code-boundary.md)，`internal/router`
作为迁移、package、逻辑快照与 native 对拍的参考模型保留，
「不是生产存储 authority」——与代码一致。

`nativerouter.Repository.Attach`（`internal/nativerouter/repository.go:134`）
**没有非测试调用方**；生产写入一律走 `StageMembership`／`StagePlannedMembership`。

## 3. 职责拆解与新归宿

| 职责 | 活的调用点 | 新归宿 |
|---|---|---|
| `OPEN ROUTE` 叶子→行 | `msql/executor/router.go:343` → `nativerouter.OpenPage` | **吸收进叶子** |
| 每条 SELECT 的 `route_paths` | `msql/executor/query.go:169,355` | **Row 的 `route_leaf_ids` 字段** + 顺 `ParentID` 算 trace |
| 删行／补偿时清空该行所有叶子 | `row/router.go:346`、`nativerow/service.go:373` | 读行上的 `route_leaf_ids` 定位，改叶子 |
| SPLIT/MERGE 重挂 | `nativemutation/service.go:378`、`route_plan.go:230` | 叶子与行的字段同事务一起改 |
| 路由计划的乐观并发 guard | `routemutationplan/build.go:303`、`model.go:71` | 改读**行自己的** `Revision` |
| 「一叶最多一活跃行」校验 | `nativerouter/repository.go:435` | 结构性成立，校验退化为断言 |
| 变更日志 `route_membership` entry | `nativechange/build.go:84`、`change/model.go:38` | 并入 `route_node` |
| 写入 API `RouteLeafIDs` 三态 | `row/model.go:50`、`nativemutation/transaction.go:187-266` | 语义保留，落地方式变 |

### 3.1 结构性消失，不需要新归宿

- **`KindStaleMembership`**（`semantichealth/route_scan.go:133`，条件
  `locator.Revision != current.Revision`）——叶子上只有 RowID、不存 revision，
  无从过期；
- **`KindInvalidMembershipScope`**（同文件 119 行，locator 的 db/table 与叶子不符）
  ——叶子自己就带 `DatabaseID`／`TableID`，不存在第二份可以对不上；
- **`KindMultiRowLeaf`**（同文件 108 行，`len(locators) > 1`）——
  一个字段放不下两个 RowID。

**这是废弃 membership 最强的理由**：三类今天要扫描、要报告、要人工修的问题，
变成了不可能发生。代价是对外可见的健康项减少三条，必须显式记入变更说明。

- **`MembershipRevision`**：核实过它**从不用于乐观并发**——只做记录 ID 后缀
  （`nativerouter/repository.go:270-282`）、latest-wins 选择（445／517／656 行）
  与变更日志的 operation 推断（`nativechange/build.go:83-96`）。
  唯一真正的冲突检查（`routemutationplan/build.go:303`）比的是
  `locator.Revision`，即**行的** revision，不是它。挂载并进叶子后，
  叶子自己的 `Node.Revision` 接管全部三项；
- **`Deleted` 墓碑**：挂载关系是独立记录时只能软删；并进叶子后就是字段清空。
  注意这会改变一处现有行为——`nativerouter/archive_test.go:81` 与
  `router/archive_test.go:38` 固化了「删叶子/子树时刻意保留 membership 记录」，
  那种解耦随字段化消失，迁移时要重新表达这两条测试的意图。

## 4. `router.Node` 加 RowID 字段

模型加一个字段（空串 = 该叶子未挂行；非叶子恒为空）。编码有现成先例可循：

`encodeNode`／`decodeNode`（`internal/nativerouter/repository.go:669-726`）里，
**`Synopsis` 就是后加的可选尾部**——`encodeNode` 无条件追加（688 行），
`decodeNode` 用 `if input.offset < len(payload)` 兼容加它之前写的旧记录（719 行），
末尾再校验 `input.offset == len(payload)`。

RowID 沿用同一手法：无条件追加在 Synopsis 之后，读取端同样用偏移守卫兼容旧记录。
**不要**扩 `input.texts(8)` 那个定长文本数组（697 行）——那会让旧记录直接解码失败。

bucket 版的 `putNode`／`getNodeAny`（`internal/router/service.go`）作为对拍参考模型
同步跟进，否则 parity 测试会先红。

## 5. 反查：Row 上的一个默认字段

「给定 RowID，找它挂在哪些叶子下」在三处活路径上被需要，不能丢：
每条 SELECT 的 `route_paths`、删行时的叶子清理、SPLIT/MERGE 重挂。

**决定：业务行加一个默认字段 `route_leaf_ids`，只存稳定的叶子 ID。**

理由是写入顺序本来就给了它：写入形态 §2 的次序是
「分配 RowID → 把 RowID 挂到叶子 → 再写真实数据」，
所以**写数据那一刻挂载已经确定**——`row.WriteOptions.RouteLeafIDs`
（`internal/row/model.go:50`）现在就在手里。直接填进行里即可，
不需要再造第三个结构去回答一个写入时已知的问题。

> **修订记录（2026-08-22）**：本节原先定的是"单独一棵反向索引树"。
> 那个方案作废——它要维护一个写入时已知的映射，是多余的结构。
> 一并订正：把该方案称作"反向索引树"是转述失真，
> 原始意见是"建一张表存对应关系、靠 B+ 树快速取"。

### 5.1 两边都不存路径，trace 实时算

`router.Node.ParentID`（`internal/router/model.go:25`）**已经存在**——
语义树本来就是双向的。从叶子顺 `ParentID` 走到根即得完整 trace。

**任何被记下来的路径都会过期**：SPLIT／MERGE／RENAME 都是一等 MSQL，
语义重构是这个产品的日常，不是例外。所以行上只存叶子 ID，叶子上只存 RowID，
**两边都是稳定 ID，谁都不存路径**。

### 5.2 顺带解掉一个今天就存在的问题：`Node.Path` 这个缓存

同样的道理适用于 `Node.Path` 自己——它就是一份被物化的 trace，维护代价是实测的：

`RenameRouterNode`（`internal/nativerow/service.go:1082-1093`）现在会
`routes.Nodes()` **全树扫描** → 按 path 前缀匹配 → 重写每个子孙的 `Path`
**并给每个子孙 `Revision++`** → 一个事务里全部 stage。
**改一个分支的名字要重写整棵子树。** 参考实现那边是
`repathDescendants`（`internal/router/service.go:256`），递归做同一件事。

去掉 `Path` 之后，RENAME 退化成改一个节点，`repathDescendants` 整个消失。

**取舍要说清楚，不能只讲好处**：读路径会从"拿到节点即有 path"变成
"顺 `ParentID` 走 depth 次点查"。fanout 上限 12（F223）、语义树本就该浅，
但 **depth 目前没有上限**。

- **实施前先量一次真实深度**，并考虑要不要给 depth 设上限；
- 若读代价不可接受，**退路是把 `Path` 降级为可过期的建议值、以 `ParentID` 链为准**，
  而不是退回全树重写。

### 5.3 两份指向的一致性

叶子存 RowID、行存 leaf ID——同一份事实存了两遍，命中
[架构原则](../product/architecture-principles.md) §2 的判据 2。
**这是一个有意的例外，必须写明而不是默认**：

- 两边都在**同一次写入的同一个事务**里落盘，不存在"两个结构各自演化"；
- 换来的是两个方向都零额外结构、零额外查找；
- 与 membership 的区别在于：membership 是一个**独立的对象**，有自己的
  revision 和墓碑、能独立于两端漂移；这里两端都是既有对象上的字段。

## 6. 对外可见面的变更

| 面 | 变化 | 兼容判断 |
|---|---|---|
| SELECT 的 `route_paths` | **行为不变**，换数据来源 | 无感知 |
| `RouteLeafIDs` 三态（`nil`=保持／`[]`=清空／非空=替换） | 语义保留，落地方式变 | 无感知；wire 面 `protocol/msql/protocol.go:83-100` 不动 |
| `OPEN ROUTE` | 行为不变，少一次查找 | 无感知 |
| **Route 的 revision** | **挂行／摘行会推高叶子的 revision** | **有感知，见下** |
| 变更日志少一种 entry kind | `change.ObjectRouteMembership`（`change/model.go:38`）不再产生，改由 `route_node` 表达 | **已发布格式**：旧 envelope 必须继续解码；`Validate` 的 kind 白名单（同文件 184／216 行）保留该值 |
| 语义健康少 3 类问题项 | 见 §3.1 | **能力减少**，需在发布说明中点名 |

### 6.1 一个原文漏掉的可见变化：Route revision 会被数据写入推高

**原文这张表写了「无感知」，不成立。** 实现时才发现：

节点记录按 revision 作键（`nativerouter/repository.go` 的 `nodeRecordID`），
`StageNode` 要求 `Revision == latest.Revision+1`——**同 revision 重写在结构上
不可能**。叶子记下它持有的 Row，那就是对节点的一次修改，必然带来新 revision。

于是 `ALTER ROUTE` / `DELETE ROUTE` 的 `EXPECTED REVISION` 会被一次数据写入
打断：你按 revision 1 读了叶子，有行挂了上去，你的编辑就撞版本冲突。
`TestDeletingARouteLeafRequiresItToBeEmpty` 第一次就是这样红的——本该报
「叶子里还有行」，实际报的是版本冲突。

**接受这个后果，不做例外**，理由三条：

1. 要不推高 revision，就得给节点第二个版本计数器——那正是本设计要删掉的
   `MembershipRevision`，换个名字重新引进来等于白做；
2. `Node.Revision` 的含义就是「这条节点记录变了」。**一个现在持有 Row 的叶子
   确实和之前不一样了**，告诉并发编辑者这件事是对的，不是噪声；
3. [F169](../planning/f169-single-row-route-leaf.md) 保证一个叶子至多挂一行，
   所以这是叶子一生中至多发生两次的事，不是每次写入都抖。

**客户端契约**：编辑一个叶子前重新读它的 revision，不要沿用创建时那个。
这条要进发布说明，和三类语义健康项一起。

## 7. 分阶段与验证门

每阶段一条独立可验证的性质。generation 版本号 +1、既有库开机 COW 自动升级，
沿用 `internal/pagestoremigration` 已有的「从已提交 Record 构建 generation」，
不另起炉灶。

| 阶段 | 内容 | 独立可验证的性质 |
|---|---|---|
| 1 | `router.Node` 加 RowID 字段与编解码（两套实现同步） | 旧记录逐字解码不变；新旧混存可读 |
| 2 | Row 加 `route_leaf_ids` 字段，与 membership **双写** | 两个来源对同一 RowID 返回相同叶子集合 |
| 3 | 读路径切到叶子字段 + 行字段 | `OPEN ROUTE`／`route_paths`／`SHOW ROUTES` 逐字一致，且不再读 kind 9／13 |
| 4 | 写路径停写 membership；变更日志改发 `route_node` | 新事务不产生 kind 9／13 记录；旧 envelope 仍可解码 |
| 5 | 删除 kind 9／13、`Attach`、`ValidateMembershipChanges`；`ObjectKindMax` 下调；generation 升版 | 既有库自动升级，内容逐字不变 |
| 6 | 删除三类健康项与 `internal/router` 的死 membership 代码 | 见下 |
| 7 | trace 改为顺 `ParentID` 实时算，删掉 `Node.Path` 与 `repathDescendants` | RENAME 一个分支只写一个节点（今天要写整棵子树）；`route_paths` 与 `SHOW ROUTES` 逐字一致 |

阶段 7 **有一道前置量测**：先测真实语义树深度与 `route_paths` 的读代价。
量完再决定是彻底删 `Path`，还是把它降级为可过期的建议值（见 §5.2 的退路）。
这一阶段独立于 1–6，可以单独排。

阶段 6 受[旧代码清理边界](../development/legacy-code-boundary.md)的删除规则约束：
删除前必须证明目标不在 `cmd/...` 生产依赖图中，并先增加「旧路径不得重新出现」的 RED。

**跨阶段的逐字一致基线**：切换前后比对 `OPEN ROUTE`、`SHOW ROUTES`、
带 `route_paths` 的 `SELECT`、`SHOW CHANGES`、语义健康报告。

## 8. 待定

- 阶段 4 之后 `change.ObjectRouteMembership` 在 `Validate` 白名单里保留多久，
  与整体的已发布格式支持窗口一起定；
- `docs/planning/f224-mandatory-row-route.md`（候选：写入时强制语义索引）
  建立在 membership 之上，需按本文重写后再评估。

## 关联

- [写入形态](../product/write-model.md)（上位规范）、[查询形态](../product/query-model.md)
- [存储层总览](./README.md)「已知偏差」C 组
- [语义 Router](../query/semantic-routing.md)、[Route Read v1](../query/route-read-v1.md)
- [Route Mutation Plan v1](../query/route-mutation-plan-v1.md)、
  [Route Mutation Execution v1](../query/route-mutation-execution-v1.md)
- [F169：Leaf 单 Row 不变量](../planning/f169-single-row-route-leaf.md)（不变量保留）
- [语义健康 v2](../agent/semantic-health-v2.md)、[旧代码清理边界](../development/legacy-code-boundary.md)
