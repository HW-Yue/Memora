# AI-native 可演化配置

状态：F79 已实现首批查询预算；自动优化策略仍推迟到最后阶段讨论。

## 原则

影响语义质量和使用效果的数值不能散落为不可见的代码常量。代码可以提供启动默认值；Database 安装或创建后，当前有效配置成为数据库自身的一部分，能够被发现、版本化、打包和审计。

```text
启动默认值
→ 写入 Data Dictionary
→ 按配置生命周期决定冻结或允许变更
→ 若允许变更，再经过 MSQL / revision / Policy
→ 按条件运行 benchmark、提交或回滚
```

配置不是 prompt 偏好，也不是进程内临时变量。陌生 Agent 和恢复流程必须能够读取同一份当前配置及历史。

## 首批配置对象

- 当前 Router 分支、叶子 locator、SELECT 扫描/返回和
  Route Frame 预算已进入 `query_budgets`；
- Table 级 Router fanout、深度和字符预算由 F126 的真实模型能力曲线提供证据；
- 查询回表 Row 数、关系遍历和输出预算；
- Column 级文本最大字符数，启动默认值 1200；
- 后续经验证适合自治的 alias、关系扩展和缓存策略。

既有 `0.8/0.2`、`query_terms`、`index_terms` 配置属于已撤销混合检索原型，
不进入新 Database。`1200` 等仍有效的数值是启动配置，不是隐藏的永久代码常量。
但“存入数据库”不等于“建库后都允许修改”。

## 生命周期分类待定

每项配置最终必须归入一种生命周期，具体分类推迟到最后阶段讨论：

- 建库时确定，之后冻结；
- 只能通过显式迁移修改；
- 允许用户在运行时修改；
- 允许 AI 在满足条件时优化；
- 属于引擎或安全不变量，数据库不能覆盖。

在分类冻结前，不承诺 AI 能自动修改任何配置，也不把“可配置”与“可自治优化”视为同义词。

F79 的 `query_budgets` 暂归为“允许显式运行时修改”：必须完整替换五项预算，
提交 expected revision、actor 和 reason。它不代表 AI 获得无证据自动调参权限。

```sql
SHOW CONFIGURATION;
SHOW CONFIGURATION HISTORY LIMIT :limit;
ALTER CONFIGURATION QUERY_BUDGETS SET
  OPEN_LOCATORS :locators,
  SELECT_SCAN :scan,
  SELECT_ROWS :rows,
  ROUTE_FRAME_NODES :frame;
RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION :revision;
```

恢复会追加一个新的补偿 revision，并记录 `restored_revision`；不会删除历史或把
当前指针静默倒退。引擎读取当前 revision 约束 `SHOW ROUTES`、`OPEN ROUTE` 和
`SELECT`。`route_frame_nodes` 由宿主 Skill 在跨语句 Route Frame 中执行，
数据库负责给所有宿主返回同一当前值。

启动默认值是 Route children 12、locator 1、SELECT scan 1000、SELECT rows 10、
Route Frame nodes 12。F169 后 `open_locators` 是兼容字段：历史 revision 可以保留较大
数值，但它不能突破每个 Leaf `0..1` 的产品基数。引擎另保留不可由配置突破的资源安全上限。
其中 12 只是当前启动默认值，不代表模型准确率已经证明它最优或安全。
完整 revision 链进入 logical snapshot，因此复制、迁移和 Database package 不会
悄悄退回宿主默认值。

## 第二个配置键：`route_policy`

**`route_children` 已于 2026-09-22 随路由分页一起撤掉**：`SHOW ROUTES` 现在一次返回整层，
读侧不再有"一页取多少"这个选择，层的体量由结构上限唯一决定。撤掉它的理由是两条键管同一个数字
必然产生隐式不变量（`route_children ≥ branch_fanout`），一旦有人把 fan-out 调到 20 而读侧还是 12，
就会出现"一棵合法却读不出来的树"。所以**fan-out 只在 `route_policy.branch_fanout` 一处治理，
读侧对它没有意见**。结构上限是独立配置键 `route_policy`，拥有独立 revision 链、actor 和 reason：

```sql
SHOW CONFIGURATION ROUTE_POLICY;
SHOW CONFIGURATION ROUTE_POLICY HISTORY LIMIT :limit;
ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :fanout;
RESTORE CONFIGURATION ROUTE_POLICY TO REVISION :revision;
```

`branch_fanout` 的启动默认值是 12，取值范围 `2..100`。它同样归为「允许显式运行时
修改」：Agent 在 Route branch 越界失败后自行判断重构子树还是提高上限，两条出路
都写在失败信封里。降低上限不回溯，既有超限子树保持可读可维护。规则见
[写入形态](./write-model.md) §4.3。

**作用域要说准（2026-09-22 核查）**：现在它**不是每库一份**——`mem_config` 里只有
`memora.configuration.route_policy` 一个键，`SHOW CONFIGURATION ROUTE_POLICY` 也不带
Database 列，所以一个实例只有一份 route policy；`query_budgets` 同样如此。但引擎自己的
措辞写的是「this database allows %d」（`internal/nativeconfig/policy.go` 的
`ValidateFanoutStep`），失败信封又只在某个库的写入里出现，读起来像按库配置。**待定**：
把措辞改成"this instance"，还是把 route policy 真正做成每库一份（`mem_config` 的
key 带上数据库 id，`SHOW`/`ALTER`/`RESTORE` 都要接受库名）——后者是协议改动，需要
单独一块授权。在那之前，Skill 按"实例一份"写。

裸 `SHOW CONFIGURATION` 仍返回 `query_budgets`；两个键不接受对方的字段，也不能因为
某次目标恰好等于读取预算就复用成一个含义含混的开关。两个键的写入都必须带
`expected_revision`（先读再写），否则引擎直接拒。

## 配置记录

每项配置至少保存：

- 稳定配置键、作用域和当前值；
- config revision 与 expected revision；
- actor、reason、创建和更新时间；
- 触发调整的指标或反馈摘要；
- 生效范围、兼容版本和回滚目标；
- 最近验证结果与是否仍处于 candidate 状态。

Database 级配置随该 Database 的 Data Dictionary 迁移。Column 级配置属于 Schema，也随 revision 历史迁移。

## 调整边界

对于生命周期允许修改的配置，AI 只能通过声明式 MSQL 操作，不能直接改内存变量、配置文件、索引文件或系统表物理记录。配置变更经过 Parser、Policy、影响预算和 revision 校验，并返回结构化收据。

AI 优化的触发条件、证据窗口、benchmark、审批、观察期和自动回滚策略尚未确认，统一留到最后阶段讨论。

## 不可交给 AI 的不变量

以下内容只能由版本化引擎或明确的管理员安全策略改变：

- 事务原子性与 SQLite 恢复正确性；
- 权限上限、隐私隔离和审批等级；
- SQLite 文件格式内部；
- 系统字段、revision 链和引用完整性；
- 防止资源耗尽、损坏和越权的最终安全边界。

AI-native 表示语义策略可持续学习，不表示物理正确性可以动态猜测。

## 关联

- [AI-native 产品契约](./ai-native-contract.md)
- [自描述 Data Dictionary](../data/self-describing-data-dictionary.md)
- [MSQL](../query/msql.md)
- [AI 自主权与约束](../archive/agent/autonomy.md)
