# 当前的问题

**这里是唯一的活账。** 一个还没解决的问题一个文件，本页是状态表。
带日期的审查/评测报告（`audit-*`、`dogfood-*`、`acceptance-*`）是**证据**，不是账；
它们的结论如果还没做完，就必须在这里有一条并链回证据。

状态只用三种：**待做** / **在做**（指到分支） / **已关**（指到提交或结论）。已关的条目留在表里，
不删行，免得下次重复怀疑。

## 未解决

| 编号 | 问题 | 严重度 | 证据 | 已定的修法 | 状态 |
|---|---|---|---|---|---|
| [A6](./A6-verified-flag-comes-from-readback.md) | 两个 L2 回执无条件 `verified: true`，`VerificationCode` 从不赋值 | high | [audit](./audit-2026-09-23.md) A6 · `route_plan.go:36-39`、`schema_plan.go:25-28` | 提交后只读读回，比对计划声明的目标 | 待做 |
| [B1](./B1-membership-moves-bypass-the-write-path.md) | 叶子间搬迁走裸 `UPDATE`，不落 revision/history/变更日志 | medium | [audit](./audit-2026-09-23.md) B1 · `route_plan.go:211-249` | 改走正常行写路径；`MembershipMove.Revision` 按 informational 处理 | 待做 |
| [B2+B3](./B2-B3-tree-cycle-guard-and-hidden-failures.md) | 计划校验不查环、执行期父链遍历无 hop 保护；解析失败被静默吞掉 | medium-high | [audit](./audit-2026-09-23.md) B2/B3 | 校验补环检测 + 两处父链加 hop 上限且不再静默 | 待做 |
| [B5](./B5-repair-recall-units-never-converges.md) | `REPAIR RECALL UNITS` 对「live 行、叶子数 ≠ 1」永不收敛 | medium | [audit](./audit-2026-09-23.md) B5 · `recall.go:311-314`、`:81-83` | 第二轮必须与第一轮不同：收敛或明确 `blocked` | 待做 |
| [B6](./B6-instance-destroy-fails-open.md) | `instance destroy` 把 Inspect 的错误当成「没在运行」 | medium | [audit](./audit-2026-09-23.md) B6 · `instance.go:59-66` | 只做 fail-closed，不铺开租约 | 待做 |
| [B7](./B7-daemon-has-no-tests.md) | `internal/daemon` 920 行、零测试 | medium | [audit](./audit-2026-09-23.md) B7 | 先补三条不变量；之后每条修复顺手加 | 待做 |
| [P1](./P1-catalog-ddl-ignores-the-mutation-block.md) | Catalog DDL 静默忽略 `mutation` 块：宿主以为记了 actor/reason，其实没有 | medium | [decisions.md](../decisions.md) 2026-09-25 尾 | 不受支持的语句显式拒绝 `mutation` 块 + 回归测试 | 待做 |
| [P4](./P4-table-purpose-and-row-semantics-are-welded.md) | Table 的 `purpose` / `row_semantics` 焊死在建表那一刻 | medium-high | 用户 2026-09-26 边界 · `parser.go:835` | 补 `ALTER TABLE … SET`（与库级同形）；**先定 `row_semantics` 的去留** | 待做 |

## 已关闭（留档，别重复怀疑）

| 编号 | 问题 | 结论 | 证据 |
|---|---|---|---|
| [C2](./C2-mutate-does-not-drain-vectors.md) | `memora mutate` 不排干向量（而 Skill 推荐用它） | 已修：`runMutate` 提交后调用与 `exec` 同一个排干，授权用 plan 自己的范围 | `fdd5f8ef`（merge `dfffc398`）· 行为测试 `TestMutateDrainsPendingVectorsLikeExec` |
| [C1](./C1-write-md-root-detection-is-wrong.md) | Skill 教错「空数组 = 没有根」 | 已修：判据改成建根那一步的拒绝；并钉住「空页在有无根时相同」与拒绝措辞 | `c3665196`（merge `af1ba112`）· `TestRootBootstrapIsLearnedFromTheRefusal` |
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
