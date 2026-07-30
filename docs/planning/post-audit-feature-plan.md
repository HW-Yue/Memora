# 真实审计后 Feature 计划

状态：已批准并实施中；来源为
[AI-native 真实使用审计](./ai-native-live-ux-audit-2026-07-30.md)。

所有 Feature 都必须通过公开 Skill/CLI 和原生 authority；内部 Go 单测不能单独
证明产品故事完成。每次 Row mutation 后从 Table 顶层 Route 重新导航验证。

## F73 原生 UPDATE + Route

- 目标：非空完整 membership 快照与 Row/History/locator 同事务更新；
- 覆盖：US-UPDATE、US-CORRECT、US-ENGINE；
- 门：清空后可重新挂载，重启后 locator revision 与 Row 一致；
- 状态：已实现。

## F74 原生 History 补偿

- 目标：`RESTORE ... TO REVISION` 与 `AS OF COMMIT_SEQUENCE`；
- 覆盖：US-DELETE、US-CORRECT、US-RECOVER；
- 门：删除、历史读取、补偿 revision、Route 恢复、重启重查闭环。
- 状态：已实现。

## F75 原生 Feedback 与 Semantic Health

- 目标：移除 `legacyRows` 依赖，接入原生 Rows/Catalog；
- 覆盖：US-CORRECT、US-DBA；
- 门：feedback record/confirm 与 health report 在发行二进制可运行。
- 状态：已实现；CLI E2E 覆盖 feedback record、确认撤销、Route 再发现与
  semantic health report。

## F76 公开 Split/Merge/Route Reshape

- 目标：将原生 coordinator 暴露为版本化 MSQL/Skill Mutation Plan；
- 覆盖：US-SPLIT、US-OPTIMIZE、US-ENGINE；
- 门：source、targets、relations、上层 Route 和 memberships 原子改变。
- 状态：已实现；公开 MSQL 与 Mutation Plan 均走原生 coordinator，E2E 覆盖
  Split→关系迁移→上层 Route revision→Merge→Route 再发现，故障注入覆盖全回滚。

## F77 快速冷启动与按需 Route Synopsis

- `DESCRIBE TABLE ... COMPACT` 返回紧凑 Column 摘要，不再清空 Columns；
- 默认 Route Frame 仍只返回短 `purpose`；
- branch 可保存版本化、约 300–1000 字符的 `synopsis`，只在含混时显式读取；
- synopsis 描述当前私有子树的 scope、anti-scope 和选择提示，不作为事实答案；
- 覆盖：US-COLD、US-READ、US-HUMAN。

## F78 来源强度与真实隔离复核

- 区分 conversation assertion、repository/document anchor 和 reviewed source；
- 对从资料产生的事实要求 Source Receipt，普通 INSERT 不伪装成已复核事实；
- reviewer 隔离不能只靠两个自报 context ID；
- 覆盖：US-ASSIMILATE、US-CONFLICT、准确性原则。

## F79 数据库内可演化配置

- Route、locator、SELECT 和上下文预算进入可发现、版本化配置；
- 变更带 expected revision、actor、reason 和回滚目标；
- 覆盖：AI-native 产品契约第九项。

## F80 真实发行故事门

- 构建发行二进制和隔离 Instance，执行全部用户故事 transcript；
- mutation 后必须从顶层 Route 重新发现，禁止已知 RowID 旁路验收；
- Codex 与 Claude adapter 共享协议，并分别执行冷启动 transcript；
- 只有公开旅程全部通过才恢复产品级 `PASS`。
