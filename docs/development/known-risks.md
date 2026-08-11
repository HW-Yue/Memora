# 已知风险

状态：2026-08-11 建立。收录已确认存在、但未被现有 Feature 文档记录的问题。
按严重度排序。每条给出代码位置和判断依据，不写推测。

修好一条就从这里移除并写进[系统能力](../product/system-capabilities.md)。

## 严重：会导致产品主张不成立

### 1. Query Agent 只有一步记忆，多跳导航结构上不可能

`internal/agent/query_agent_wire.go:42` 的 `makeQueryProviderRequest` 每轮最多构造 4 条消息：
system prompt、user（问题 + Bootstrap Frame）、assistant（**上一次**工具调用）、
tool（**上一次**结果）。`query_agent.go` 循环里 `previousCall` 与 `previousResult` 是
**覆盖**而非追加。

后果：第 3 轮看不到第 1 轮的调用与结果。需要「OPEN ROUTE → 收窄 → SELECT」的多跳查询
在结构上无法完成，模型也会重复已经失败过的查询，因为它没有尝试过的记录。

这直接解释了 `post-f204-development-plan.md` 里那句「下一步不是扩大样本，而是先修正
Query Agent 的多轮导航/SQL 选择行为」，也解释了真实运行 9/12 与 3/12 的结果。
这不是模型能力问题，是循环实现问题。

### 2. 第一条 SELECT 就终止导航，哪怕它返回零行

`query_agent.go` 的 `if len(result.Evidence) > 0 { choice = ProviderToolChoiceNone }`：
一旦 `result.Evidence` 非空，工具调用被强制关闭，模型必须立即作答。

而 `selectEvidence()`（`query_agent.go:346`）只要求 `Statement == "SELECT"` 且
`Status == succeeded` 且 `Error == nil`——**不检查行数**。一条返回零行的 SELECT
同样算作 evidence，同样立刻锁死后续导航，逼模型凭空作答。

第 1、2 条叠加后，Query Agent 实际可用的推理深度接近一次性猜测。默认预算
（`MaxProviderCalls: 4`、`MaxToolCalls: 3`）在这两条约束下形同虚设。

### 3. AI-native 的全部主张压在最薄的一层上

约 14 万行是工程扎实的常规数据库；差异化主张（AI 自主语义建模优于 chunk + embedding）
完全落在 `internal/agent` 的约 2.3 万行里，而这一层既有第 1、2 条的结构缺陷，
也从未产出过可用的质量数字。

这不是代码缺陷，是投入结构问题，记在这里是为了不让它被 F 编号的完成度掩盖。

## 中等：会在真实使用中暴露

### 4. 文档解析的配置上界与实现能力差约 50 倍

实测放大倍率为峰值堆 ≈ 正文 7 倍（约 2.6 KB 堆／IR 节点）。而配置默认：

- `DefaultEPUBAdapterConfig`：`MaxTotalUncompressedBytes: 512 MiB`；
- `DefaultPDFAdapterConfig`：`MaxFileBytes: 128 MiB`、`MaxDecompressedBytes: 512 MiB`。

按 7 倍推算，一个刚好卡在上界的文档需要约 3.5 GB 堆。上界没有拦住会打爆 daemon 的输入，
它**允许**这类输入。典型文档（正文 1–5 MiB）完全没问题，问题只在上界本身没意义。

### 5. 解析未流式化，抵消了 SourceStore 的流式设计

F191 明确「上传全程流式处理，不把整本书读进内存」，用 32 KiB buffer 写盘。
但解析阶段：

- `pdf_adapter.go:147` 把整个 PDF 一次 `io.ReadAll` 进内存；
- `epub_adapter.go:283`／`docx_adapter.go` 的 `archive.cache[locator]` 缓存每个读过的条目，
  **无淘汰、无上限**（只受归档声明总量约束）。

流式纪律止步于存储边界，解析阶段全部退化为整载。

### 6. 两个读路径去抢单写者门

`internal/pagestoremigration/authority.go` 的 `writeGate` 是容量 1 的信号量（`:133`），
全实例写串行——对本地单用户这是合理取舍。但 `ListCommittedChanges`（`:237`）与
`GetCommittedChange`（`:271`）这两个**读**操作也调 `BeginWrite`，会被任意写阻塞；
其余读路径走的是 `lockRead` 的 RWMutex。若是有意为之（change log 需序列化读），
应加注释说明；否则是可摘除的耦合。

⚠️ [F220](../planning/f220-query-working-set.md) 的精确失效方案依赖
`ListCommittedChanges`，**必须先修这条**，否则工作集每 turn 校验都会与写入竞争。
F220 Stage 1 因此采用保守全丢，绕开该依赖。

### 7. MSQL Session 无数量上限与空闲回收

`internal/msql/service/service.go:66` 的 `OpenSession` 对同一 id 复用，对新 id 无条件创建，
`sessions` map 没有容量上限，也没有空闲超时。正常断连由
`databaseHandler.SessionClosed`（`daemon/execute.go:789`）回收，所以常规使用不泄漏。
但长连接内轮换 session id 的调用方会让 map 单调增长。当前是本地单用户，风险低，
属于"应加上界"而非"正在出问题"。

## 轻微：工程卫生

### 8. CI 只有 macOS runner

`.github/workflows/ci.yml` 只跑 `macos-latest`。项目在 Linux 上可编译、全测试通过
（本次已验证），但没有 Linux CI 保护，darwin-only 假设会静默漏过。
加 `ubuntu-latest` 到 matrix 成本接近零。

### 9. 没有 linter

只有 `gofmt -l` 与 `go vet`。16 万行代码建议加 `golangci-lint`，至少
staticcheck／errcheck／ineffassign 三项。全库 `panic()` 仅 1 处、`TODO/FIXME` 为 0，
基线很干净，正适合上 linter 保持。

### 10. 单文件过大与占位分支

`cli/cli.go` 1,820 行、`msql/parser/parser.go` 1,759 行、`agent/pdf_adapter.go` 1,300 行。
AGENTS.md 对文档定了约 150 行上限，对代码没有对等约束。

`parser.go:28` 有一个空的 `if parser.matchKind(lexer.KindSemicolon) {}`，
块内只有一句「F12 将会…」的注释。行为正确（消费尾随分号后要求 EOF），但写法会误导，
应改为直接调用或删除。

### 11. 唯一的 panic 在可达路径上

`internal/store/treecontrol/control.go:44` 的 `EncodeBootstrap` 在 `Encode` 失败时
`panic(err)`。当前调用方传的都是合法 spaceID，实际不可达；但它是全库唯一的 panic，
改成返回 error 可以让「生产代码零 panic」成为可断言的不变量。

## 关联

- [系统能力](../product/system-capabilities.md)
- [路线 v2](../planning/roadmap-v2.md)
- [语义重建的不对称性](../data/semantic-rebuild-asymmetry.md)
- [ADR-0010 小规模高质量评测](../decisions/0010-small-scale-high-quality-evaluation.md)
