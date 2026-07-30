# ADR-0002：v0 不内置 Agent Runtime

状态：Accepted，F43。

## 背景

`memora ask` 会让 daemon 自带模型 Provider、Agent loop、token/费用预算、密钥
存储和外部数据发送 Policy。它只有在外部 Skill 路径存在无法弥补的产品缺口，
或用户明确需要脱离 Codex/Claude Code 独立使用时才值得进入 v0。

## 证据

F30–F42 已经通过 Canonical Skill 和统一 MSQL 覆盖 query、write、Schema、
conversation、conflict、assimilation、maintenance、feedback、installation 与
benchmark；Codex 和 Claude Code adapter 绑定相同 Skill/contract digest，并跑
同一数据与 revision 结果。当前覆盖审计没有 Skill-first 功能缺口。

尚无独立使用需求的 benchmark 或用户证据；Provider 凭据/隐私规格与 token、
延迟、费用、步数、mutation 预算也未冻结。此时内置模型只会重复宿主能力，
同时增加密钥和网络攻击面。

## 决策

v0 不实现 `memora ask`、Provider 接口、模型配置或 secret 字段。daemon 保持
确定性 MSQL/Policy/事务引擎；模型继续由 Codex/Claude Code 提供。这个决定不
阻塞本地离线数据库读写，也不允许把宿主 API Key 复制进 Memora 配置。

F43 的 gate 只会在以下三项同时有证据、且 Skill/双宿主/五类 benchmark 仍无
覆盖缺口时，把状态提升为 evaluate_candidate，而不是自动批准开发：

1. 外部宿主无法满足的独立使用场景已验证；
2. Provider 凭据、隐私、外部发送和离线行为已冻结；
3. token、延迟、费用、最大步骤和 mutation 预算已验证。

## 结果

v0 用户无需另配模型 API Key，Release 与安全门不承担 Provider 风险。未来若
重新开启，内置 loop 仍必须生成标准 MSQL，不能绕过 Parser、Policy 或逻辑
收据；当前进程配置也不能通过兼容扩展保存明文密钥。

## 关联

- [可选内置 Agent Runtime](../agent/embedded-agent-runtime.md)
- [进程配置与宿主边界](../development/process-configuration.md)
- [AI-native Benchmark v1](../development/ai-native-benchmark-v1.md)
