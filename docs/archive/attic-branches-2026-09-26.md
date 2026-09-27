# 2026-09-26：清掉的分支，以及每条为什么可以清

**不是当前规格。** 这份记录只为两件事：以后想知道"那条分支是什么、去哪了"时不用考古；
以及证明它们**不是**被随手删掉的。

背景：ADR-0011 之后主线换过两次名字（pre-rewrite `main` → `rewrite/adr0011` → `main`），
仓库里留下了一批不再指向任何现行工作的分支。清理日：2026-09-26。删除时本地还剩 7 条
非主线分支（`main` 之外的）。

## 先确认过的前提

- **`main` 是唯一主线**（2026-09-26 起）。pre-rewrite 那条 `main` 的提交**全部包含在主线的历史里**
  （当年是一次快进），所以它作为分支名已经没有信息量。
- `rewrite/adr0011` 与 `main` **同指一个提交**（`b6fb313a`）——两个名字指同一条线，
  是真会漂移的那种冗余，因此一并退役。
- 下面五条分支的**主题**在主线上都有归属：文档类进了 `docs/archive/`，代码类要么已由别的实现落地，
  要么整个包在 ADR-0011 重写里被删掉了。

## 删掉的分支

| 分支（原 tip） | 独有提交 | 它是什么 | 为什么可以删 | 现在的立场在哪 |
|---|---|---|---|---|
| `d2587ae1` | 0 | **pre-rewrite `main`**（旧主线） | 提交全部包含在主线历史里 | 主线自身 |
| `9f44ca53` → `b6fb313a` | 0 | **`rewrite/adr0011`**（主线旧名） | 与 `main` 同指一个提交 | 就是 `main` |
| `e5030500` | 3 | `docs/embedded-agent-assimilation-architecture`：内嵌 assimilation agent 架构、单页摄取、源交互、运行时语言（+392 行文档） | 内置 Agent 已被 **ADR-0002 延后**、**ADR-0009 改成薄 Agent Loop**；文档已归档 | `docs/archive/agent/embedded-agent-runtime.md`、`docs/archive/data/assimilation.md`、`docs/archive/planning/assimilation-agent-feature-sequence.md` |
| `a2cc2d0d` | 1 | `docs/external-evaluation-scorecard`：外部评测 scorecard + 评测 agent 可观测性 | 评测路线由 **ADR-0010** 取代（小规模、高质量），文件已归档 | `docs/archive/development/evaluation-agent-observability.md`、`docs/archive/planning/future-roadmap.md` |
| `4f5ba2be` | 1 | `feat/admin-markdown-document-node`：Admin 语义画布的文档节点渲染行 Markdown（`bundle.js`/`css`） | 能力已在主线**以另一实现落地**：`markdown-it` + `DOMPurify`，测试断言 `documentNodeHTML` | `internal/adminui/dist/assets/routes.js`（`markdownFragment`）、`internal/adminui/bundle_test.go` |
| `ce595056` | 3 | `feature/F180-openai-compatible-provider`：引擎内 OpenAI 兼容 HTTP provider + 495 行契约测试 | 架构已定：**模型调用与向量由宿主做，Memora 不发网络请求、不持有 base URL 与 key**；`internal/agent/` 整个包在重写里已不存在 | `docs/archive/planning/f180-openai-compatible-provider.md`；`AGENTS.md`「当前产品原则」 |
| `39554ba0` | 1 | `feature/f203-ocr-evidence-gate`：F204 外部 agent hook 契约测试（只有测试，无实现） | 大文档/PDF/图片不作为持久化内容（产品原则）；hook 面已归档 | `docs/archive/planning/f204-external-agent-hook.md` |

## 代价（要认）

上面五条分支共 **9 个提交只在本地**（远端没有对应分支）。它们被删后不再被任何引用指着，
`git gc` 之后就不在了。**这份文件保留了它们的 tip 与内容概要**，但代码/文档正文没有另存副本——
判断依据是"主题在主线上有归属"这一列。如果将来要复用其中某段实现，得从这份记录认出是哪一条，
而正文已经没了。

## 没删的

`main`（唯一主线）。此外没有保留任何 `attic/`、`archive/` 名字的分支——历史靠提交、
不靠分支名。
