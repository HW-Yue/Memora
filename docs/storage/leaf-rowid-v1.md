# 语义索引叶子直挂 RowID：membership 的废弃与迁移

状态：**迁移设计**（2026-08-22）。落实[写入形态](../product/write-model.md) §2
「叶子直接挂 RowID，不再有独立的 membership 对应关系」，不是独立规范——
与写入形态冲突时以写入形态为准。**尚未排期，未开始实现。**

编写原则同[存储层总览](./README.md)：每条「现状」断言都能指到具体文件与行，
指不到的不写。

## 一句话

`router.Node` 上没有能放 RowID 的字段，于是叶子到行的对应被做成了一个独立的、
自带版本号和墓碑的记录类型。**给叶子加上那个字段，这个记录类型整体退场**——
它的职责一部分被吸收，一部分改由反向索引树承担，还有一部分结构性地消失。

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
| 每条 SELECT 的 `route_paths` | `msql/executor/query.go:169,355` | **反向索引树** |
| 删行／补偿时清空该行所有叶子 | `row/router.go:346`、`nativerow/service.go:373` | **反向索引树**定位，改叶子 |
| SPLIT/MERGE 重挂 | `nativemutation/service.go:378`、`route_plan.go:230` | 改叶子，反向索引同事务更新 |
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

## 5. 反向索引树

「给定 RowID，找它挂在哪些叶子下」在三处活路径上被需要，不能丢：
每条 SELECT 的 `route_paths`、删行时的叶子清理、SPLIT/MERGE 重挂。

**决定：单独一棵二级索引树**，键 `row_id`、值 leaf_id 列表。
本质是把第 13 号 object kind 换个存法保留下来。它只存指向、不存正文，
不违反「同一份内容只存一处」。

不选「业务行存 `route_leaf_ids` 列」是因为那会让叶子和行两边各存一份指向，
必须在同一事务里维持一致——多一个可以不一致的地方，就多一类
`KindStaleMembership` 那样的健康项，与本次改动的初衷相反。

实现参照现有二级索引：`internal/store/objectindex`（已建好但尚无调用方）与
`internal/store/catalogindex`。与叶子改动**同一事务**提交，走 `treecommit.Runtime`。

## 6. 对外可见面的变更

| 面 | 变化 | 兼容判断 |
|---|---|---|
| SELECT 的 `route_paths` | **行为不变**，换数据来源 | 无感知 |
| `RouteLeafIDs` 三态（`nil`=保持／`[]`=清空／非空=替换） | 语义保留，落地方式变 | 无感知；wire 面 `protocol/msql/protocol.go:83-100` 不动 |
| `OPEN ROUTE` | 行为不变，少一次查找 | 无感知 |
| 变更日志少一种 entry kind | `change.ObjectRouteMembership`（`change/model.go:38`）不再产生，改由 `route_node` 表达 | **已发布格式**：旧 envelope 必须继续解码；`Validate` 的 kind 白名单（同文件 184／216 行）保留该值 |
| 语义健康少 3 类问题项 | 见 §3.1 | **能力减少**，需在发布说明中点名 |

## 7. 分阶段与验证门

每阶段一条独立可验证的性质。generation 版本号 +1、既有库开机 COW 自动升级，
沿用 `internal/pagestoremigration` 已有的「从已提交 Record 构建 generation」，
不另起炉灶。

| 阶段 | 内容 | 独立可验证的性质 |
|---|---|---|
| 1 | `router.Node` 加 RowID 字段与编解码（两套实现同步） | 旧记录逐字解码不变；新旧混存可读 |
| 2 | 反向索引树落地，与 membership **双写** | 两个来源对同一 RowID 返回相同叶子集合 |
| 3 | 读路径切到叶子字段 + 反向索引树 | `OPEN ROUTE`／`route_paths`／`SHOW ROUTES` 逐字一致，且不再读 kind 9／13 |
| 4 | 写路径停写 membership；变更日志改发 `route_node` | 新事务不产生 kind 9／13 记录；旧 envelope 仍可解码 |
| 5 | 删除 kind 9／13、`Attach`、`ValidateMembershipChanges`；`ObjectKindMax` 下调；generation 升版 | 既有库自动升级，内容逐字不变 |
| 6 | 删除三类健康项与 `internal/router` 的死 membership 代码 | 见下 |

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
