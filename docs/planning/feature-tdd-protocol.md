# 小 Feature TDD 与合入协议

状态：已确认；适用于当前 SQLite 基座上的所有 Feature、修复和架构迁移。

## 分支隔离

- 禁止直接在默认分支开发或形成工作提交；当前默认分支是 `main`，规则同样覆盖名为 `master` 的默认分支。
- 每项 Feature 从最新默认分支创建 `feature/<short-name>`；修复和纯规范调整分别使用独立的 `fix/`、`docs/` 分支。不要再使用 `F169` 这类旧引擎流水号。
- 一个开发分支只承载一个主要结果，不夹带下一 Feature 或无关清理。
- 合入前先同步默认分支并处理冲突，再重跑本 Feature 要求的全部门禁；只有全绿且 Review 通过才能合入。
- 默认分支只接收已验收的原子提交，不保留故意失败的 RED 测试或半成品实现。

## Feature 大小门

一个 Feature 只能有一个主要结果，并且必须：

- 能用一句话说明新增行为；
- 有一个独立 RED 测试入口；
- 可以在不依赖下一 Feature 的情况下验收；
- 可以单独 revert，不留下第二套半成品路径；
- 不同时跨越两个独立故障域，例如「向量索引重建 + 词法 posting 事务」。

出现以下任一情况，编码前拆分：两个独立用户旅程、两个持久化协议、两个恢复
算法、必须用“以及/顺便”才能描述目标，或测试无法指出究竟哪项能力失败。
生产代码约 400 行、三个核心 package 只是拆分警报，不是把复杂 Feature 合法化的
额度。

## RED

开工记录必须列出：

- 测试名称与命令；
- 输入 fixture、故障点和期望稳定错误/状态；
- 当前为什么失败；
- 失败应证明缺少目标能力，而不是编译错误、随机时间或坏 fixture；
- 本 Feature 明确不覆盖的相邻行为。

至少先有一个最小 RED。存储格式、恢复和并发 Feature 必须在实现前同时列出完整
failure matrix，随后可逐条转绿。

失败测试只存在于本地 Feature 工作过程，不把故意红灯提交到 `main`。

## GREEN 与 REFACTOR

1. 只实现使当前 RED 变绿的最小行为；
2. 每增加一个分支，补相应失败或边界测试；
3. GREEN 后才能重构；重构不得改变协议、错误码或持久化字节；
4. 删除旧路径时先有“旧路径不可再到达”的测试；
5. 不以 mock 全绿代替真实 file/daemon/reopen 集成证据。

## 测试类型

| Feature 类型 | 强制测试 |
| --- | --- |
| SQLite 表 / 事务 | reopen、中断、错误注入、串行写与读最新提交 |
| 词法 postings | 写入同事务可见、删除不可达、分词边界 |
| 向量 / vec0 | kind 隔离、提交后异步可见性、embedding 失败 fail-closed |
| MSQL/API | parser/binder/executor contract、golden envelope、权限/预算 |
| Admin | 组件状态、API contract、浏览器旅程、空/错/截断/权限状态 |
| AI Benchmark | 固定 suite、真实模型 receipt、原始计数、重跑与缺失标记 |

随机测试必须保存 seed；时间、ID、I/O 和调度必须可注入。Fuzz target 的 seed corpus
进入普通测试，长时间 fuzz 在专门任务运行。

## 每项合入门

至少执行：

```text
targeted package tests
go test ./...
go test -race ./...
go vet ./...
format / generated / golden checks
feature-specific fault, fuzz-seed or browser suite
```

若全量 race 在当前 CI 超过预算，必须保留全量定期任务，并在 Feature PR 对受影响
package 执行 race；不能静默跳过。

完成证据包含测试命令、关键 case、用户故事/内部不变量、实际结果、未覆盖项和
commit。没有 reopen/中断证据的持久化 Feature、没有真实模型 receipt 的 AI Benchmark，一律 `INCOMPLETE`。

## 执行顺序

```text
Review Feature
→ 产品门 PASS
→ RED evidence
→ minimal GREEN
→ boundary/fault tests
→ REFACTOR
→ full gates
→ 完成门 PASS
→ 合入 main
→ 下一 Feature
```

Milestone 只组织顺序，不允许把多个 Feature 合成一次无法定位失败的大实现。

## 关联

- [Feature 产品与用户故事门禁](./feature-product-gate.md)
- [历史 TDD 开发总计划](../archive/planning/tdd-development-plan.md)
- [当前 Feature 状态](../archive/planning/feature-status.md)
- [后续路线](../archive/planning/future-roadmap.md)
