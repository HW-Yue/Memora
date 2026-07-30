# F72 AI-native 用户故事门

状态：结构门完成，但产品级 `PASS` 已由 2026-07-30
[真实使用审计](./ai-native-live-ux-audit-2026-07-30.md)撤销。该门只能证明语法和
proof 文件存在，不能证明公开 Skill/CLI 主旅程已接通。

本门禁不使用相似度 benchmark。它把产品宪章中的每个 `US-*` 绑定到可解析的
MSQL transcript 和仓库内实际执行该行为的测试；`internal/storygate` 会拒绝故事
缺失、重复、无 proof、无效 MSQL 或旧检索路径。

## 标准主旅程

```text
SHOW DATABASES
→ SHOW TABLES FROM <db>
→ DESCRIBE TABLE <db>.<table>
→ SHOW ROUTES FROM TABLE <db>.<table> AT ROOT LIMIT 12
→ SHOW ROUTES UNDER <route-id> LIMIT 12（逐层）
→ OPEN ROUTE <leaf-id> LIMIT 24
→ SELECT ... WHERE row_id = <row-id> LIMIT 1
```

Route Frame 最多 12 个节点，叶子 locator 最多 24 个，回表最多 10 Row；中间层
不包含正文。写入只提交完整 Route membership 快照，不存在语义词项或另一套
自动检索兜底。

## 故事证据

| 故事 | PASS 证据 |
|---|---|
| US-HUMAN | Canonical Skill 与 Codex 端到端自然语言宿主 fixture |
| US-COLD / US-READ | 原生 Table Router MSQL 与 Skill 查询状态机 |
| US-INSERT / US-UPDATE / US-DELETE | CLI/daemon vertical slice、原生 Row/History |
| US-CORRECT / US-CONFLICT | feedback revision 与 conflict view/用户裁决测试 |
| US-SCHEMA | 原生 Catalog MSQL 与 Schema Plan 测试 |
| US-DBA / US-OPTIMIZE | review-only health report 与局部 Route reshape |
| US-SPLIT | 原生 split/merge 单事务 Row、History、Route membership 测试 |
| US-ASSIMILATE | coverage、隔离复核、锚点、提交和 Source Receipt 测试 |
| US-RECOVER | transaction frame 截断恢复与重开后逐层导航 |
| US-ENGINE | 跨对象故障回滚、revision/schema/权限门 |
| US-DEVELOPER | Feature 产品门、CI 全门禁和退役路径扫描 |

精确 proof 文件由 `storygate.ReleaseEvidence()` 维护；路径不存在会直接失败，避免
文档声称通过但测试已被删除。

## 宿主与 Provider

Codex 和 Claude Code 继续共享同一 Skill/协议 digest。模型由宿主管理：
OpenAI-compatible endpoint 可以是 Kimi/CC Switch 等任意用户配置地址，不假定
OpenAI 官网；Claude Code 可走 CC Switch 的 Anthropic-compatible 配置。Memora
永不接收 Provider 地址或密钥。

## 许可

根目录与两个宿主 bundle 均携带 PolyForm Noncommercial 1.0.0 和商业授权说明：
个人及其他许可内非商业用途免费；任何商业用途须事先取得单独书面付费许可。

## 结论

F72 的结构检查本身完成，但不能据此宣称所有产品故事通过。发行二进制实测发现
UPDATE 无法携带非空 Route 快照、空快照会令 Row 不可发现，RESTORE、feedback
与 semantic health 的原生公开路径也未接通。产品结论改为 `FAIL`，修复后必须用
公开 Skill/CLI transcript 重新验收。Page、B+ Tree、Buffer Pool、MVCC、Undo/Redo
和并发内核仍是后置候选，不因本门禁自动进入路线。
