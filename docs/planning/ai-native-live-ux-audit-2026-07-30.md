# AI-native 真实使用审计

状态：完成；产品级结论 `FAIL`；2026-07-30。

本审计使用提交 `56cc73f` 构建的 `0.1.0` arm64 发行二进制和隔离 Instance，
只通过 Canonical Skill 允许的 CLI/MSQL 操作，不读取物理数据库文件。由于主
Agent 看不到 SubAgent 的完整隐藏推理上下文，本轮未把结论委托给 SubAgent。

## 判定原则

按产品宪章和质量模型检查：值得写、写得对、组织得对、逐层找得到、返回短、
改得准、能回滚、陌生 Agent 能接管。重点对应“快、可修改、准确”。

## 实测通过

- 新建 Database/Table、Schema 同义候选复用、带 Route membership 的 INSERT；
- Database → Table → Route 逐层导航 → locator → RowID SELECT；
- Router 中间层不返回正文，权限越界返回 `permission_denied`；
- expected revision 过期写入返回 `revision_conflict`；
- revision 历史读取、daemon 重启和原生持久化；
- macOS arm64/amd64 release smoke 与 arm64 clean-machine 八步验收。

## 阻断问题

### P0：修改与语义索引不能同时正确

公开 UPDATE 携带当前非空 `route_leaf_ids` 时返回无细节的 `internal_error`；改用
空快照可以提交 revision 2，但 `OPEN ROUTE` 随即返回空，Row 变得不可发现。
因此 US-UPDATE、US-CORRECT、US-SPLIT 和“修改优先于追加”不成立。

### P0：删除后不能公开恢复

DELETE、History 和 `AS OF REVISION` 可用，但 RESTORE 返回 `internal_error`；
`AS OF COMMIT_SEQUENCE` 同样未实现。US-DELETE 的补偿路径和 US-RECOVER 不完整。

### P0：原生 authority 切换后产品能力未接齐

`memora feedback --event` 返回“feedback state has not migrated to the native
authority”；`memora maintain --report` 返回“semantic health source is
unavailable”。原生 split/merge coordinator 只有内部 Go API 和单元测试，没有
CLI/MSQL/Skill 入口。因此 US-CORRECT、US-DBA、US-OPTIMIZE、US-SPLIT 未通过。

### P1：F72 门禁产生假阳性

`storygate.Validate` 只解析示例 MSQL；测试只检查 proof 路径存在。E2E UPDATE
显式传空 Route 快照，修改后只按 RowID SELECT，没有重新从顶层 Route 验证。
内部 reshape/feedback 单测不能替代原生发行二进制的公开主旅程。

### P1：冷启动 Schema 与速度仍不合格

`DESCRIBE TABLE ... COMPACT` 按实现清空全部 Columns，AI 必须再发一次非紧凑
DESCRIBE。三层 Route 的一次真实读取共 9 个 CLI 调用、约 9.9 KiB JSON、引擎
本地耗时约 0.55 秒；模型逐层决策的实际端到端时间会更长。五个项目 Row 的首次
建模需要 1 个 Schema Plan、9 个 Route mutation 和 5 个 Mutation Plan。

### P1：准确性仍主要靠宿主自律

普通 INSERT 可以保存从文档概括出的事实而没有 source anchor；本轮首次演示即
直接写入了项目摘要，Engine 没有阻止。Assimilation 的“独立上下文”只比较两个
用户提供的 context ID 字符串，不能证明 reviewer 真是干净上下文。

### P2：可演化配置尚未入库

Route 12、locator 24、SELECT 10 和上下文 12000 等预算仍位于 Skill contract/
代码常量，没有 Database 内可发现、版本化、审计和回滚的配置对象。

修复状态：F79 已将 Route children、locator、SELECT scan/rows 和 Route Frame
nodes 写入原生 `query_budgets` revision 链，并接入公开 MSQL、真实执行路径、
补偿恢复和 daemon 重启验收。审计总状态仍等待 F80 真实发行故事门后统一更新。

## 结论与重新验收条件

当前已证明“原生 INSERT + 逐层 Route + RowID SELECT”最小读写闭环，但不能称为
完整 AI-native 产品。修复顺序应为：

1. 接通原生 UPDATE/RESTORE 与 Route membership 原子闭环；
2. 把 feedback、semantic health、split/merge 接到同一公开 MSQL/Skill 路径；
3. 用真实发行二进制重写故事门，每个 mutation 后必须从顶层 Route 重查；
4. 修复紧凑 Schema，减少调用与上下文；
5. 增加来源强度、真实隔离复核和数据库内配置的产品验证。
