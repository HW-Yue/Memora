# F198：Author / Reviewer 隔离的独立语义复核门

状态：已完成，2026-08-05。

## 唯一主要结果

对 F196 的一个 extent claim batch 发起全新、无作者会话历史的 reviewer Provider 请求，并把
每个 claim 的数字、真实 source anchor、草稿内冲突和“是语义模块而非原文窗口复制”四项复核
绑定为不含正文的持久 artifact。本项只决定 accepted/rejected，不提交 MSQL。

## 隔离边界

- reviewer actor 必须与 batch 内唯一 author actor 不同；构造时相同即拒绝；
- 每个 review 只发一次新 Provider request，messages 固定为新 system+user，不传 author
  assistant/tool history；Provider port 仍厂商中立；
- 由注入的 challenge source 产生 256-bit challenge，reviewer 必须原样回传；artifact 仅保存
  challenge SHA-256；测试使用确定性 challenge，生产默认使用 `crypto/rand`；
- 没有可信宿主签名时，这仍是可验证的请求隔离与身份分离，不夸大为密码学上
  证明了云端模型没有隐藏共享状态。

## 四项检查

1. `anchor_verified`：Runtime 先确定性验证 claim node/anchor 与当前 `ReadExtent` 逐字节
   一致；reviewer 必须回传相同有序 node ID；
2. `numbers_verified`：Runtime 递归提取候选参数中的 JSON 数字和字符串数字 token，
   生成 path/value 仅请求内可见的 evidence ID；reviewer 必须完整回传并判定受锚点支持；
3. `conflict_free`：reviewer 同时获取当前 batch 和调用方提供的有界同文档 peer claim，
   检查内部重复/矛盾；现有 Database 状态的最终对账属于 F199；
4. `not_raw_copy`：reviewer 判定候选是完整可修改的语义模块，而不是机械 chunk/整窗口复制；
   artifact/store 的字节检查另外保证不持久化 extent、MSQL、参数、prompt 或 raw response。

reviewer 还必须对每个 claim 返回 `accept|reject|needs_user` 和有界 finding code。只有全部 claim
decision=accept、四项全 true、node/evidence inventory 完整且无 finding 时 artifact 才是 accepted；
其余合法复核结果持久化为 rejected，`needs_user` 可由编排器转成 F197 branch issue。

## Artifact 与恢复

artifact 绑定 review/job/Database/document/extent/batch SHA-256、有序 peer claim SHA-256、author/reviewer、
review provider/model/prompt/challenge、usage、每 claim digest、node ID、numeric evidence ID、四项布尔结果和 finding code。

`AssimilationReviewStore` 以 Job+extent 派生文件名，使用 `0700` 目录、`0600` 文件、临时文件
fsync+原子 rename+目录 fsync。同 input 重放 artifact 不调 Provider；同 Job+extent 换 input 冲突；
checksum 或严格 JSON 损坏 fail closed。拒绝 artifact 也持久化，防止通过无限重试 reviewer 偷换结果。

## TDD 与完成门

RED 先锁定：新鲜两消息 required tool call、actor 隔离、challenge echo、数字 inventory 完整、
anchor 精确相等、peer digest 绑定、accepted/rejected 分流、非法/漏项输出不产生 artifact、
reopen/replay 无调用、input 冲突、corruption 与持久字节无正文，以及 race/import guard/全量 CI。

## 完成证据

- `AssimilationReviewer` 为每个非空 extent batch 创建固定 system+user 的全新 required-tool
  请求；reviewer 与唯一 author 相同会在 Provider 调用前拒绝；
- 生产 challenge 使用 `crypto/rand` 生成 256 bit 值，模型必须精确回传；artifact 只保存其
  SHA-256，不保存 challenge、prompt、extent、候选 MSQL、参数或 raw response；
- Runtime 先核对 claim anchor 与真实 `ReadExtent`，再递归生成数字 path/value inventory；
  reviewer 输出必须按原顺序完整回传 claim、node 和 evidence ID；
- 全部四项检查和 decision/finding 共同决定 accepted/rejected；合法拒绝也原子持久化，
  避免用重复调用挑选更宽松结果；
- Artifact Store 使用 `0700/0600`、严格 JSON、checksum、fsync 与原子发布；reopen 后同 input
  不再调用 Provider，同 Job+extent 换 input 冲突，损坏 fail closed；
- 16 个并发调用只产生一次 Provider/challenge 调用，其余返回同一 checksum 的 replay；目标测试、
  race、vet、Agent import guard 与全量 unit/integration/E2E/cross-build CI 全绿。

完成门结论：PASS。下一项为 F199 短事务 reconciliation、in-doubt 恢复与 Source Receipt。

## 关联

- [F196 Draft / Claim Ledger](./f196-draft-claim-ledger.md)
- [F197 分支暂停/恢复](./f197-branch-pause-resume.md)
- [资料独立复核与提交](../agent/assimilation-review-v1.md)
