# F196：可替换 Provider 的 Draft / Claim Ledger

状态：已完成，2026-08-05。

## 唯一主要结果

将一个完整 `ReadExtent` 交给可替换 Provider 一次，生成一组可恢复、可审计但尚未获准的
claim 与候选 MSQL。本 Feature 不提交数据库、不做独立语义复核，也不开启批量真实模型评测。

## 边界

- Agent 只依赖标准库和 `protocol/msql`；Catalog/Route 导航仍经注入的 `MSQLExecutor`；
- Provider 只经现有厂商中立 `Provider` port，`provider_id` 和 `model` 是配置与证据，
  不把 DeepSeek/Kimi SDK 或类型引入 Agent 核心；
- 每个 extent 最多一次 completion，严格 required tool-call 一次返回多个 claim；
- 账本是 Agent-owned 临时操作状态，不是用户 Database 的真相源，不绕过 F195 `REVIEW/SUBMIT`；
- 不单独持久化 extent 原文、完整 prompt 或 Provider 原始响应；候选 MSQL 及其语义参数
  是后续复核所需的有界草稿，允许存入账本。

## 合同

`AssimilationDrafter.Draft` 接收 Job/作者/目标 Database/文档来源与完整 `ReadExtent`：

1. 用 extent 中的有界文本生成最外层导航 query，经 MSQL Bootstrap 获取当前 Catalog/lexical/root frame；
2. 将紧凑 frame 与完整 extent 编码为一个有界 Provider request，强制
   `propose_assimilation_claims` tool；
3. 模型只返回 claim ID、source node ID、候选 MSQL/参数、Schema/revision/affected-row
   guard 和 Route 提示；actor、source locator/hash 与 document digest 由 Runtime 注入；
4. source node ID 必须属于当前 extent，Runtime 从 IR 复制稳定 anchor，模型不得自造 anchor；
5. 严格解码、上限、锦标重复、参数结构和候选完整性校验全部通过后，才把整个
   extent 的 claim batch 作为一条原子 ledger record 持久化。

每个 claim 至少绑定：`job_id`/`claim_id`、Database、document SHA-256、extent sequence/SHA-256、
source node+anchor、`provider_id`/`model`、prompt version/SHA-256、候选 `AssimilationStatement` 和 claim SHA-256。
extent receipt 记录 Provider usage 和有序 claim ID。

## 恢复与安全

- 同 Job + extent sequence + extent digest 重试直接返回原 receipt，不再请求 MSQL/Provider；
- 同 sequence 换 digest、同 Job 重复 claim ID 或同 claim ID 换内容必须 fail closed；
- journal 使用 `0600` 文件、checksum chain、append+fsync；只有末尾未完整记录可截断恢复，
  中部损坏和 checksum 篡改拒绝打开；
- Provider 错误、length finish、非单 required tool-call、未知 node 或不合法 candidate 不留部分账本；
- F196 只生成未审批草稿；F198 独立 reviewer 与 F195 结构 review 仍可拒绝它。

## TDD 与完成门

RED 先锁定：

- scripted MSQL + scripted Provider 的单 extent 多 claim golden，且 completion 恰好一次；
- 模型只给 node ID，账本 anchor 与 extent 逐字节一致；
- reopen/replay 不调 Provider，sequence digest 冲突和并发重试不重复写；
- 无原文/prompt/raw response 的持久化字节检查，torn-tail 恢复和中部 corruption fail closed；
- 非法 tool/anchor/guard/超额输出不产生 record；Agent import guard、目标 race 与全量 CI。

完成门为上述证据全绿，且以 `review_required` 前状态导出有序 claim，不执行 F195
`SUBMIT ASSIMILATION`。

## 完成证据

- `AssimilationDrafter` 经注入 MSQL Bootstrap 获取紧凑导航 frame，对每个 extent 使用一次
  required `propose_assimilation_claims` tool call，支持一次返回多个或零个 claim；
- Kimi/DeepSeek 只是相同 Provider port 上的 `provider_id`/`model` 配置；核心无厂商 SDK
  或新网络依赖；
- Runtime 严格解码 tool JSON，校验 claim/guard/参数上限，并仅允许当前 extent
  node ID；稳定 anchor、actor 与 source provenance 全由可信 Runtime 注入；
- 每个 `review_required` claim 绑定 Job/Database/document/extent/provider/model/prompt、
  source node+anchor、候选 `AssimilationStatement` 与自身 SHA-256；
- `0600` checksum-chain journal 按 extent 原子追加并 fsync；reopen 与并发重试不重复
  MSQL/Provider 调用，digest 冲突拒绝，torn tail 可恢复，中部篡改 fail closed；
- 持久化字节不包含原始 extent、完整 prompt 或 raw Provider response；模型错误与非法输出
  不留部分 record；
- 目标测试、Agent import guard、race、vet 与全量 unit/integration/E2E/cross-build CI 全绿。

完成门结论：PASS。本项没有发起真实模型批量评测；下一项为 F197 问题驱动的分支暂停与恢复。

## 关联

- [ReadExtent 与 coverage](./f194-read-extent-coverage.md)
- [正式 MSQL 吸收面](./f195-msql-assimilation-surface.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
