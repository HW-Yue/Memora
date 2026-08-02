# Memora 文档入口

这里只保留当前有效设计的入口。完成门、旧计划、被取代方案和讨论过程统一放在
[`archive/`](./archive/README.md)，不能作为当前实现依据。

## 从这里开始

- [当前产品基线](./product/current-product.md) — 产品现在是什么、已经能做什么、还缺什么；
- [Feature 状态](./planning/feature-status.md) — Feature 的权威完成、撤销、证据不完整和延期账本；
- [后续路线](./planning/future-roadmap.md) — 真实 AI 证据、内置评测 Agent、外置 Hook 和后期候选；
- [F169 之后的开发序列](./planning/post-f169-development-plan.md) — 倒排索引、最小内置 Agent、
  外部答案测评、可替换 Provider 写入和长资料吸收的逐项顺序；
- [查询 Agent Feature 序列](./planning/query-agent-feature-sequence.md)与
  [资料吸收 Agent Feature 序列](./planning/assimilation-agent-feature-sequence.md) — 两条独立垂直链；
- [AI-native 产品宪章](./product/ai-native-product-charter.md) — 最高层产品原则和永久边界；
- [Feature 产品门](./planning/feature-product-gate.md)与
  [TDD 协议](./planning/feature-tdd-protocol.md) — 新开发的拆分、授权和验收规则。
- [F169：Route Leaf 单 Row 不变量](./planning/f169-single-row-route-leaf.md) — 修复 Leaf
  候选桶缺陷，冻结一个 Leaf 最多一个活跃 Row。
- [ADR-0008：全内容倒排索引](./decisions/0008-full-content-inverted-index.md) — 当前 Row 与
  语义索引进入可重建 lexical postings，最终事实仍由 SQL 回表；
- [F170：全内容倒排语义模型](./planning/f170-inverted-index-surface.md) — 已完成的无 I/O reference index；
- [F171：持久化 Posting Store](./planning/f171-persistent-posting-store.md) — 已完成的 Page/WAL/B+ Tree 物理层。
- [F172a：Row Posting Generation](./planning/f172a-row-posting-generation.md) — 已完成的 Row 投影、generation v2 与 v1 COW 升级。
- [F172b：Live Row Posting Publication](./planning/f172b-live-row-posting-publication.md) — 已完成的在线 Row posting 原子替换与恢复。
- [F173a：Catalog Posting Publication](./planning/f173a-catalog-posting-publication.md) — 已完成的 Catalog seed、在线替换、删除 tombstone 与恢复。
- [F173b1：Route Posting Generation](./planning/f173b1-route-posting-generation.md) — 已完成的 Route 投影、generation v3 与 reopen reconciliation。
- [F173b2：Live Route Posting Publication](./planning/f173b2-live-route-posting-publication.md) — 已完成的 direct/plan/reshape 统一 Route publication。

## 当前产品规格

### 数据与产品

- [AI-native 产品边界](./product/ai-native-boundary.md)
- [AI-native 产品契约](./product/ai-native-contract.md)
- [语义记录模型](./data/semantic-records.md)
- [自描述 Data Dictionary](./data/self-describing-data-dictionary.md)
- [资料吸收](./data/assimilation.md)
- [Database Package](./product/database-package-v1.md)
- [质量模型](./product/quality-model.md)

### Agent 与查询

- [Canonical Skill](./agent/canonical-skill-v1.md)
- [MSQL](./query/msql.md)
- [语义 Router](./query/semantic-routing.md)
- [Route Branch Fan-out 策略](./query/route-branch-fanout-policy.md) — Database 自治目标、
  Agent 语义重构与可审计例外的候选规则；
- [检索质量链路](./query/retrieval-quality.md)
- [投机 Route 预取](./query/speculative-route-prefetch.md)
- [CPU 精确 Route Match](./query/cpu-exact-route-match-v1.md)
- [Skill 写入](./agent/skill-write-v1.md)
- [Schema 生命周期](./agent/skill-schema-lifecycle-v1.md)
- [Policy Enforcement v2](./development/policy-enforcement-v2.md)
- [MCP Adapter](./agent/mcp-adapter-v1.md)
- [可选内置 Agent Runtime](./agent/embedded-agent-runtime.md)
- [Agent 的 MSQL 边界与依赖注入](./agent/agent-msql-dependency-injection.md)

### 存储与可靠性

- [原生 Store](./storage/native-minimal-store.md)
- [Page/Record 布局](./storage/tablespace-page-record-layout.md)
- [Buffer Pool](./storage/buffer-pool.md)
- [MVCC、Undo 与 Redo 边界](./storage/mvcc-undo-redo.md)
- [持久化 B+ Tree 索引](./storage/indexing.md)
- [Page Store Authority](./storage/page-store-authority-v1.md)
- [Page Index Generation v3](./storage/page-index-generation-v3.md)
- [Change Log 与未来同步](./storage/binlog-and-sync.md)
- [备份](./storage/instance-portable-backup-v1.md)、
  [恢复](./storage/instance-restore-v1.md)与[搬迁](./storage/instance-move-v1.md)

### 使用与观察

- [CLI Workflow](./development/cli-database-workflow.md)
- [Go SDK](./development/go-sdk-v1.md)
- [macOS LaunchAgent](./development/macos-launch-agent-v1.md)
- [Admin Shell](./development/embedded-admin-shell-v1.md)
- [Route Trace](./query/route-trace-read-v1.md)
- [评测 Agent 与外置 Hook](./development/evaluation-agent-observability.md)
- [旧代码清理边界](./development/legacy-code-boundary.md)
- [签名发布制品](./development/macos-signed-release-artifacts-v2.md)
- [干净机器验收](./development/clean-machine-acceptance-v1.md)

## 决策与历史

- 当前 ADR 保存在 [`decisions/`](./decisions/)；新决策必须注明 Accepted、Superseded 或 Deferred；
- 市场快照保存在 [`research/`](./research/)，不是实现规格；
- 历史方案和完成证据只从 [归档入口](./archive/README.md)追溯；
- 日常任务不要批量读取归档文档。
