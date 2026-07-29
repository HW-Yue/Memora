# TDD 开发总计划

状态：执行中。

当前进度：F00–F19 与 Phase A 退出测试已完成；下一项为 F20 机械索引。

## 产品目标

第一条发布链路不是“做完数据库内核”，而是验证：

```text
Codex / Claude Code 安装 Skill
→ Skill 安装并启动本地 Memora
→ 用户自然讨论项目或提供资料
→ Agent 自主发现、总结、建模并用 MSQL 维护
→ 新 Agent 能查询、修订和接管
```

用户不管理 Schema、Router、索引或事务。Skill 只在语义冲突、高风险、越权和不可恢复操作时请求用户。

## 实现顺序

1. 冻结可测试契约并建立 Go 测试骨架；
2. 完成本地 daemon、MSQL 和可替换原型存储；
3. 完成语义检索、Router、历史和索引维护；
4. 完成由 Skill 编排的 AI 维护、资料吸收与 Codex/Claude 适配；
5. 完成 GitHub Release、安装、升级和干净机器验收；
6. 通过 AI-native 质量门后，再替换为自研存储内核。

原型后端采用 SQLite，但只能位于 `Store` 适配层后。MSQL Parser、事务外部语义、Data Dictionary、稳定 ID、revision、Router、索引协议和 JSON envelope 都属于 Memora 契约，不得泄漏 SQLite SQL 或文件格式。进入原生内核前必须先有逻辑导出与迁移测试。

详细 feature：

- [Phase A：契约、测试骨架与本地运行时](./tdd-phase-a-foundation.md)
- [Phase B：逻辑数据库与语义检索](./tdd-phase-b-database.md)
- [Phase C：AI 自动维护与 Skill](./tdd-phase-c-ai-skill.md)
- [Phase D：发行、质量门与原生内核](./tdd-phase-d-release-kernel.md)

## 每个 Feature 的 TDD 循环

每个 `Fxx` 严格执行：

1. 从 `main` 创建 `feature/Fxx-short-name`；
2. 先写一个会因缺少目标行为而失败的测试；
3. 确认失败原因正确，而不是测试自身错误；
4. 写满足测试的最小实现；
5. 补边界、错误和回归测试；
6. 在测试保护下重构；
7. 运行 feature 测试、全量测试、race 和静态检查；
8. 更新同一 feature 影响的规格；
9. 形成一个可构建、全绿的原子 commit；
10. fast-forward 或 squash 为一个 commit 合入 `main`。

不提交故意失败的测试到 `main`。测试先行发生在本地 TDD 循环中，最终 commit 同时包含测试和实现。

## Commit 规则

- 分支：`feature/F12-row-revision`
- commit：`feat(F12): add stable row revision writes`
- 修复回归：先增加复现测试，使用 `fix(F12): ...`
- 一个 commit 只完成一个 feature；不夹带格式化或无关文档。
- 若实现超过约 400 行生产代码、修改超过 3 个核心包或需要多个独立验收点，编码前拆成新的 `Fxx`。
- 每个 milestone 结束打候选 tag，不能用 tag 代替绿色 commit。

## 测试层级

- Unit：Parser、类型、评分、状态机和纯函数；每次保存可运行。
- Contract/Golden：MSQL AST、JSON envelope、CLI 输出、Skill 模板和包 manifest。
- Integration：真实 daemon、临时 datadir、IPC、事务、重启和并发客户端。
- End-to-end：Scripted Host Harness 重放 Agent 工具调用到最终 Row，默认不访问网络。
- Compatibility：逻辑导出、旧 format fixture、升级和跨版本读取。
- Fault injection：进程终止、部分写入、磁盘满、校验损坏和日志恢复。
- Quality benchmark：多项目对话、书籍吸收、修订、冷启动和无向量召回。
- Release smoke：全新 macOS 用户环境从 GitHub Release 安装并由 Skill 完成首次写入。

真实 Codex/Claude 测试只作为受控 smoke/benchmark，不进入普通 PR 的确定性门禁。v0 不实现自己的模型 Provider，也不保存宿主模型密钥。

## 每个 Feature 的完成定义

必须同时满足：

- 新行为有先行测试，至少覆盖成功、失败和一个边界；
- 错误返回稳定 code，不靠匹配自然语言；
- 测试不读写真实用户目录，不依赖执行顺序；
- 时间、ID、模型和故障点可以注入；
- `go test ./...`、`go test -race ./...`、`go vet ./...`、格式检查通过；
- 影响协议时更新 golden fixture 和文档；
- commit 可单独 checkout、构建、测试和回滚。

## 阶段质量门

- G1：CLI/daemon/MSQL 契约稳定，重启后数据不丢；
- G2：SQL、Router、倒排、revision 和后台重建形成完整数据闭环；
- G3：Codex/Claude Skill 能自动维护项目与资料，冲突只展示不裁决；
- G4：干净 macOS 从 Release 安装，50 轮自治和冷启动接管达标；
- G5：原生内核通过崩溃恢复与迁移测试，才替换原型后端。

任何质量门失败都先修产品链路，不提前进入下一阶段的大型优化。
