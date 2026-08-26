# 派生索引解耦：fulltext 退出写入事务

状态：**已实现**（2026-08-25）。落实
[架构原则](../product/architecture-principles.md) §1（高内聚低耦合），
是[执行计划](../planning/execution-plan.md) E2。

编写原则同[存储层总览](./README.md)：每条「现状」断言都能指到具体文件与行。

## 一句话

fulltext 是**派生**索引——里面每一条都能从权威数据重算。
所以它不该在写入事务里，写入的成败不该取决于一个可以重建的东西。

## 1. 改之前的样子

一次 INSERT 写三棵树：versions、current、fulltext。前两棵是权威数据，
第三棵是算出来的。绑在一个事务里的代价是**同一个派生索引有两种故障形态**：

- 投影失败 ⇒ 写入被拒（用户的数据没进去，因为索引不高兴）；
- 提交后索引失败 ⇒ 整个发布中毒（数据进去了，但这个库不能再写了）。

## 2. 改之后

写入事务只写权威数据。fulltext 从**提交的变更日志**自己追平。

**时机不是重点，从来都不是。** 追平就跟在写入后面立刻跑
（`catchUpFulltextAfterWrite`），只是在它的**事务之外**——写入已经落盘，
后面发生什么都撤不掉它。所以没有任何地方会看到滞后，
变的只是**写入不再依赖它**。

追平失败不让已提交的写入失败，记在 `Authority.FulltextCatchUpError()`，
读路径（`SearchLexicalLocations`）会再试一次。

模板是仓库里现成的 `authorityChangeTree.reconcile`——变更日志驱动、
游标推进、批量、读时惰性触发。没有另起炉灶。

## 3. 游标存哪儿：三个选项，选了树里

增量追平要知道「我追到哪了」。changeindex 不用存这个数，
因为**它的键本身就是 commit sequence**，树里最大的键就是答案。
fulltext 的键是 `(kind, object_id)` 和 `(term, ...)`，
**没有任何一个键的含义是「commit sequence 47」**，所以必须存。

| 选项 | 好处 | 代价 |
|---|---|---|
| **A. fulltext 树自己**（选中） | 与文档**同一个 WAL 事务**，永远不会各说各话 | 多一个 key 前缀与一对接口 |
| B. `treecontrol` 控制页 | 同样原子 | 为一棵树的需要改**所有树**的磁盘布局；而且「派生索引追到哪」不是 B+ 树的属性 |
| C. 单独一个文件 | 最好写 | 放 generation 里会撞内容摘要校验（摘要正是用来证明 generation 没变过的）；放外面就**没法与文档原子** |

选 A 的理由只有一条：**只有它让游标和它描述的内容在同一个事务里。**
一个多报的游标会让下一轮追平**跳过缺口，静默漏索引**——最坏的一类故障。

**不新增 `fulltext.ObjectKind`。** 这棵树的键空间本来就按一个字节分区
（`codec.go`：object=1／owner=2／posting=3），加 `keyKindMetadata = 4`
是纯加法：`Objects()` 只扫 object 前缀、`AllPostings()` 只扫 posting 前缀，
**两者都看不见它**，业务枚举一个字不动。有测试钉住这条。

## 4. 追平投影什么

只重投影变更日志点到的东西：

| 家族 | 怎么拿 | 有界性 |
|---|---|---|
| Row | 逐个读**当前状态** | Row 是这里唯一**无界**的一族，所以逐个读，不做全库扫描 |
| Route | 整体读 Router 节点，筛出被点到的 | 有界于语义树规模 |
| Catalog | 整体重投影（database/table/column） | 有界于 schema，不是数据量 |

**一个范围内被改五次的 Row 只投影一次终态。** 重投影终态既正确
（派生索引描述的是当前状态，不是历史），又比重放便宜。

### 一个不专门处理就会漏的地方

**Router 只交出活节点**（`nativerouter/repository.go:619` 过滤 `Deleted`）。
所以「变更日志点名了、而 Router 已经没有」的 Route 就是被删了。
不处理它会留下**过期倒排项**——正是派生索引本该让其不可能发生的那类故障。

`routefulltext.TombstoneFor` 从变更条目的零件造墓碑，
因为**变更日志本身就是「这个 Route 没了」的证据**。

## 5. 开机那一趟仍然是全量

`reconcileFulltext` 保留：变更日志被回收之后，
**只在索引里活着、源里已经没有的对象是重放看不见的**，
只有对权威源做一次全量对账才能发现。

它顺手把游标记到当时的高水位——**这就是后面每一轮都能增量而不是又一次全量的原因。**

## 6. 一处必须写明的隐含契约

`PublishMutation` 现在**依赖调用方记录了变更条目**。
生产的三条写入路径都记（`nativerow/service.go` 三处、
`nativemutation/coordinator.go`、`nativecatalog/service.go`），
但这是个**隐含契约**——直接拿桩 `commit` 调 `PublishMutation` 的调用方
（只有测试这么干）不会被追平看见。

变更日志本来就是写入路径与**每一个**派生索引之间的契约：
`authorityChangeTree` 一直是这么依赖它的。fulltext 加入这个约定不是新发明。
但契约是隐含的这件事本身值得记一笔，将来若要变成显式检查，从这里开始。

## 关联

- [架构原则](../product/architecture-principles.md) §1（上位）
- [存储层总览](./README.md)、[执行计划](../planning/execution-plan.md) E2
- [候选预测器只给路径](../query/predictor-path-only-v1.md)（同一批收窄里的另一半）
