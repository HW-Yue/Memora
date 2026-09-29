# 当前的问题

**这里是唯一的活账。** 一个还没解决的问题一个文件，本页是状态表。
带日期的审查/评测报告（`audit-*`、`dogfood-*`、`acceptance-*`）是**证据**，不是账；
它们的结论如果还没做完，就必须在这里有一条并链回证据。

状态只用三种：**待做** / **在做**（指到分支） / **已关**（指到提交或结论）。已关的条目留在表里，
不删行，免得下次重复怀疑。

## 未解决

| 编号 | 问题 | 严重度 | 证据 | 已定的修法 | 状态 |
|---|---|---|---|---|---|
| [B8](./B8-inspect-deletes-the-pid-file.md) | `daemon.Inspect` 是读操作却删 PID 文件 | medium | [audit](./audit-2026-09-23.md) B7 · `lifecycle.go:110` | 改成纯读，清理交给明确的生命周期操作（顾问判断，单开一块） | 待做 |
| [A6](./A6-verified-flag-comes-from-readback.md) | 两个 L2 回执无条件 `verified: true`，`VerificationCode` 从不赋值 | high | [audit](./audit-2026-09-23.md) A6 · `route_plan.go:36-39`、`schema_plan.go:25-28` | 提交后只读读回，比对计划声明的目标 | 待做 |
| [B2+B3](./B2-B3-tree-cycle-guard-and-hidden-failures.md) | 计划校验不查环、执行期父链遍历无 hop 保护；解析失败被静默吞掉 | medium-high | [audit](./audit-2026-09-23.md) B2/B3 | 校验补环检测 + 两处父链加 hop 上限且不再静默 | 待做 |
| [P4](./P4-table-purpose-and-row-semantics-are-welded.md) | Table 的 `purpose` / `row_semantics` 焊死在建表那一刻 | medium-high | 用户 2026-09-26 边界 · `parser.go:835` | 补 `ALTER TABLE … SET`（与库级同形）；**先定 `row_semantics` 的去留** | 待做 |

## 已关闭（留档，别重复怀疑）

| 编号 | 问题 | 结论 | 证据 |
|---|---|---|---|
| [B7](./B7-daemon-has-no-tests.md) | `internal/daemon` 零测试 → 三条不变量 | 已补断连回滚（`TestADisconnectRollsBackTheOpenTransaction`）；版本偏移两侧已覆盖；Inspect 删 PID 拆成 B8 | `b9325527` |
| [B6](./B6-instance-destroy-fails-open.md) | `instance destroy` 把 Inspect 的错误当成「没在运行」 | 已修 fail-closed：Inspect 报错即拒绝删除（目录不动），只有明确「没在跑」才删 | `72fe4ad0`（merge `57653198`）· 两条测试（握住租约删 PID／锁打不开） |
| [P1](./P1-catalog-ddl-ignores-the-mutation-block.md) | Catalog DDL 静默忽略 `mutation` 块 | 已修：凡是**不记录** mutation 的语句（Catalog DDL、读、`BEGIN/COMMIT/ROLLBACK`）一律 `validation_error` 拒收，不再接受后丢掉 | `18866cf3`（merge `a97cdace`）· `TestStatementsThatRecordNoMutationRefuseAMutationBlock` |
| [B1](./B1-membership-moves-bypass-the-write-path.md) | 叶子间搬迁走裸 `UPDATE`，不落 revision/history/变更日志 | 已修：走 `advance`→`writeRow`→`appendHistory`→`rowChange`，计划的 provenance 一路传下去 | `eaa48223` · `TestMergingLeavesAccountsForTheMovedRows` |
| [B5](./B5-repair-recall-units-never-converges.md) | `REPAIR RECALL UNITS` 对「live 行、叶子数 ≠ 1」永不收敛 | 已修：这类行改计 `blocked` + `recall_units_blocked` 通知，不再冒充 rebuild，也不再删它仅有的单元 | `a644f672`（merge `1fceff90`）· `TestRepairRecallUnitsReportsRowsItCannotIndex` |
| [C2](./C2-mutate-does-not-drain-vectors.md) | `memora mutate` 不排干向量（而 Skill 推荐用它） | 已修：`runMutate` 提交后调用与 `exec` 同一个排干，授权用 plan 自己的范围 | `fdd5f8ef`（merge `dfffc398`）· 行为测试 `TestMutateDrainsPendingVectorsLikeExec` |
| [C1](./C1-write-md-root-detection-is-wrong.md) | Skill 教错「空数组 = 没有根」 | 已修：判据改成建根那一步的拒绝；并钉住「空页在有无根时相同」与拒绝措辞 | `c3665196`（merge `af1ba112`）· `TestRootBootstrapIsLearnedFromTheRefusal` |
| P5 | 会话关闭/daemon 关停会让显式事务被 database/sql 从背后结束，包装层再回滚就报 `sql: transaction has already been committed or rolled back` | 已修：`BeginTx` 用 `context.WithoutCancel(ctx)`——事务只由 Commit/Rollback/空闲计时器结束 | `81843595` · 确定性回归 `TestAnExplicitTransactionOutlivesItsCallersContext`（改前稳定复现）|
| [P2](./P2-real-ci-dies-on-a-cold-module-cache.md) | 真实 CI 挂在第一关：冷模块缓存下 `go generate` 拿到空路径 | 已修：不是头文件过期，是指令依赖了「缓存已解出该模块」这个偶然；改成先 `go mod download` | `237bb066`（merge `65ad5f34`）· CI run `36326905880` 全绿 |
| [P3](./P3-skill-installs-carry-an-unmerged-branch.md) | Skill 的四个仓库外安装位带着未合分支的文本 | 已合那条分支（2026-09-26），内外一致到「新」的一边 | merge `54a0254d`；`sync-skill.sh --check` = every copy matches |
| A1 | 表里有过归档列就永久锁死结构变更 | 已修 `d7f834bf` | [audit](./audit-2026-09-23.md) §一 |
| A2 | 写路径吞掉向量挂载错误 | 已修 `90522532` | 同上 |
| A3 | `RESTORE` 不重建召回单元 | 已修 `aa92ad09` | 同上 |
| A4 | daemon 自锁（只 `BEGIN` 的请求拖死全实例） | 已修 `85d56752` | 同上 |
| A5 | `engine_protocol` 不匹配在客户端、执行后才拒绝 | 已修 `a506ab85` | 同上 |
| A7 | `REPAIR LINKS` 把任何 readRow 错误当成「对端消失」 | 已修 `43e8ef37` | 同上 |
| B4 | 批内解析失败是否回滚取决于首个词 | 已修 `8d639564` | 同上 |

## 查过但不是问题（免得下次又怀疑）

- `routetrace` / `nativeconfig` / `store` 零测试：诊断输出与薄封装，坏了上层立刻显形。
- `vec0` 宽度：doctor 的行级字节比对看得见。
- 归档/真删的边界、rekey「单行道」、存储层没有实例锁：都是写明的设计。
- 写操作往 stderr 打非 JSON 提示（C3）：Skill 明写不许把 stderr 合进 stdout，是宿主用法。
- 在 Skill 树里 `py_compile` 制造 `__pycache__` 假 drift（C4）：流程陷阱，不是分叉。

完整论证见 [audit-2026-09-23.md](./audit-2026-09-23.md) §三 与 §五。
