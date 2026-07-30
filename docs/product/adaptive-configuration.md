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

配置不是 prompt 偏好，也不是进程内临时变量。陌生 Agent、数据库包和恢复流程必须能够读取同一份当前配置及历史。

## 首批配置对象

- 当前原生 authority 的 Router 分支、叶子 locator、SELECT 扫描/返回和
  Route Frame 预算已进入 `query_budgets`；
- Table 级 Router 深度、字符与更细粒度覆盖仍待后续证据；
- Table 级 Router 增量/子树/generation 重建阈值与 compaction 策略；
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
  ROUTE_CHILDREN :routes,
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

启动默认值是 Route children 12、locator 24、SELECT scan 1000、SELECT rows 10、
Route Frame nodes 12。引擎另保留不可由配置突破的资源安全上限。
完整 revision 链进入 logical snapshot，因此复制、迁移和 Database package 不会
悄悄退回宿主默认值。

## 配置记录

每项配置至少保存：

- 稳定配置键、作用域和当前值；
- config revision 与 expected revision；
- actor、reason、创建和更新时间；
- 触发调整的指标或反馈摘要；
- 生效范围、兼容版本和回滚目标；
- 最近验证结果与是否仍处于 candidate 状态。

Database 级配置随数据库包迁移。Column 级配置属于 Schema/Data Dictionary，也随包和 revision 历史迁移。

## 调整边界

对于生命周期允许修改的配置，AI 只能通过声明式 MSQL 操作，不能直接改内存变量、配置文件、索引文件或系统表物理记录。配置变更经过 Parser、Policy、影响预算和 revision 校验，并返回结构化收据。

AI 优化的触发条件、证据窗口、benchmark、审批、观察期和自动回滚策略尚未确认，统一留到最后阶段讨论。

## 不可交给 AI 的不变量

以下内容只能由版本化引擎或明确的管理员安全策略改变：

- 事务原子性、MVCC 和恢复正确性；
- 权限上限、隐私隔离和审批等级；
- Page/Record 格式、校验和与格式版本；
- 系统字段、revision 链和引用完整性；
- 防止资源耗尽、损坏和越权的最终安全边界。

AI-native 表示语义策略可持续学习，不表示物理正确性可以动态猜测。

## 关联

- [AI-native 产品契约](./ai-native-contract.md)
- [自描述 Data Dictionary](../data/self-describing-data-dictionary.md)
- [MSQL](../query/msql.md)
- [AI 自主权与约束](../agent/autonomy.md)
