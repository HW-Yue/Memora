# Phase C：AI 自动维护与 Skill

目标：让 Codex/Claude Code 中的用户只通过自然语言和资料使用 Memora，AI 维护数据库，用户只处理例外。

## F28 Canonical Skill 契约

先测：Skill lint、MSQL 版本匹配、命令示例 golden、禁止读物理文件、冲突边界和上下文预算。

开发：建立单一 Skill 源，定义何时发现、查询、总结、写入、吸收资料、请求用户和返回收据。

提交：`feat(F28): define canonical Memora skill`

## F29 Scripted Host Harness

先测：给定宿主对话和预期工具序列，Harness 能重放 CLI/MSQL、注入错误并验证最终数据库和用户输出。

开发：实现不调用真实模型的 Codex/Claude 行为模拟器、transcript fixture 和 Skill contract runner。

提交：`test(F29): add scripted host agent harness`

## F30 Skill 查询流程

先测：问题依次触发发现、MATCH/Route、SELECT；索引结果不能直接回答；无结果、截断和权限错误能恢复。

开发：在 Skill 中定义有界查询状态机、query_terms、回表规则、来源定位和停止条件。

提交：`feat(F30): query Memora through skill`

## F31 Skill 写入流程

先测：IGNORE/INSERT/REVISE/MERGE/SPLIT/MOVE/RELATE，每次先查后写，索引快照同事务提交。

开发：在 Skill 中定义 Mutation Plan；CLI/daemon 实现 Policy 校验、短事务和紧凑 Mutation Receipt。

提交：`feat(F31): maintain knowledge through skill`

## F32 Skill Schema 生命周期

先测：新领域自动建库/表、同义 Schema 复用、purpose/scope 必填、rename/migration 影响计划和回滚。

开发：Skill 负责查重和规划；引擎实现受限 DDL、影响上限、revision 和迁移收据。

提交：`feat(F32): evolve schemas under policy`

## F33 Conversation Delta 交接

先测：稳定结论写入、寒暄忽略、重复 event 幂等、多项目切换、checkpoint、会话结束和缺失上下文。

开发：定义宿主 Agent 传给 `memora reflect`/MSQL 的事件信封和 source_event_id 去重；不假设隐藏生命周期 hook。

提交：`feat(F33): ingest authorized conversation deltas`

## F34 Skill 语义冲突交互

先测：相互矛盾 Row 被并列展示；数据库没有 candidate/disputed；用户选择改写/保留/删除后才产生 SQL。

开发：实现 Skill conflict view、来源/revision diff 和用户指令到新 Mutation Plan 的转换。

提交：`feat(F34): escalate semantic conflicts to users`

## F35 Skill 资料清单与覆盖

先测：目录、章节、页码、表格、附件、重复窗口、未读范围和中断恢复；未完成不能报告成功。

开发：Skill 指导宿主读取资料；Memora 只保存临时 inventory、coverage ledger 和任务 checkpoint，不保存原文。

提交：`feat(F35): track source assimilation coverage`

## F36 Skill 书籍/资料吸收

先测：一本 fixture 书被拆成完整语义模块、关系和 Source Receipt；不保存原文；关键数字可追溯；冲突交给用户。

开发：Skill 编排阅读、独立复核和 SQL 提交；Memora 提供 coverage/receipt 命令与临时状态清理。

提交：`feat(F36): assimilate long-form sources`

## F37 Skill 触发的语义维护

先测：重复 Row、同义字段、超载 Router、pending_reindex 和陈旧说明能被发现；不静默做高风险修改。

开发：引擎提供确定性 health report；Skill 在会话 checkpoint 或用户要求时执行低风险维护并生成收据。

提交：`feat(F37): maintain semantic database health`

## F38 反馈与可撤销修改

先测：useful/irrelevant/stale/wrong/incomplete 反馈不直接改事实；确认后形成补偿 revision；历史可解释。

开发：实现 feedback receipt、修订计划和逻辑 undo 命令。

提交：`feat(F38): turn feedback into auditable revisions`

## F39 首次安装流程

先测：无 binary、有旧 binary、离线、有/无 Go、错误架构、checksum 失败、用户拒绝安装和重复 init。

开发：Skill 先请求授权，再按 OS/arch 下载 GitHub Release 并校验；Release 不可用时提供源码编译回退；运行 doctor。

提交：`feat(F39): install Memora safely from skill`

## F40 Codex 适配

先测：干净 Codex 环境完成安装、项目总结、自动写入、查询、修订和冲突展示；Skill 不假设隐藏 hook。

开发：从 Canonical Skill 生成/维护 Codex 包装、触发规则、命令权限和端到端 fixture。

提交：`feat(F40): integrate Memora with Codex`

## F41 Claude Code 适配

先测：与 Codex 使用同一数据和预期行为；宿主差异不能改变 MSQL 或数据库结果。

开发：增加 Claude Code 包装、安装说明和兼容测试；不复制核心规则。

提交：`feat(F41): integrate Memora with Claude Code`

## F42 AI-native 场景套件

先测：多项目 50 轮、反复修订、书籍吸收、新 Agent 冷启动和宿主切换 fixture；统计全部质量指标。

开发：建立可 replay 数据集、评分器、报告格式和 baseline adapter。

提交：`test(F42): add AI-native product benchmark`

## F43 可选内置 Agent 评估

先测：先证明 Skill 路径的覆盖缺口；只有独立使用需求明确时，才验证 `memora ask` 的 Provider、预算和密钥方案。

开发：输出是否进入内置 Runtime 的 ADR。默认结果可以是“不开发”，不得阻塞 v0。

提交：`docs(F43): decide optional embedded agent runtime`

## Phase C 退出测试

用户在 Codex 和 Claude Code 中都无需手工操作 Schema；Agent 自动维护同一 Instance；新 Agent 不读旧聊天即可接管；语义冲突始终请求用户，不由数据库裁决。Memora v0 不要求用户另配模型 API Key。
