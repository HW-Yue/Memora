# Memora

Memora 是由 AI 自主建模、通过版本化 MSQL 读写的本地个人数据库。

项目已经形成可安装、可持久化、可通过 Skill/MCP 使用的产品原型。本地 `memora` daemon
长期承载数据库 Instance 和统一 MSQL 执行引擎；Codex/Claude Code 按 Memora Skill
通过 CLI 或 MCP 连接 daemon：

```text
memora --stdio        为外部 Agent 提供长驻 JSONL 会话
memora exec <msql>    直接执行 MSQL
memora mcp            提供唯一 memora_execute 工具
memora admin          打开本地只读观察界面
memora daemon         管理本地常驻服务
```

所有 Agent 操作必须经过同一套 MSQL Parser、Policy、事务和执行器；Agent 不能直接操作 Page、索引或日志。自带模型的 `memora ask` 属于 v0 发布后的可选评估，不阻塞 Skill-first 产品。

当前产品、Feature 状态和后续路线见 [`docs/README.md`](./docs/README.md)。

## 当前实现状态

F00–F150 与 F152 的当前主线能力已经实现；F151、F153–F163 完成证据门后延后。
F127 的真实双宿主 AI evidence 仍不完整，内置评测 Agent 与外置 Hook 属于下一阶段。
权威账本见 [Feature 状态](./docs/planning/feature-status.md)。当前 CLI 提供
Instance/daemon、MSQL query/exec、Skill 写入与 Schema 计划、资料吸收、Database Package、
Wiki 导出、格式升级和诊断链路。基础入口包括：

```text
memora help
memora init
memora init --data-dir /absolute/path
memora init --instance work --log-level debug
memora daemon start --data-dir /absolute/path
memora daemon status --data-dir /absolute/path
memora daemon stop --data-dir /absolute/path
memora upgrade --plan --data-dir /absolute/path
memora upgrade --apply --yes --data-dir /absolute/path
memora doctor repair --yes --data-dir /absolute/path
memora version
memora version --json
```

升级 apply 与 doctor repair 都是显式高风险操作：Agent 必须先展示只读计划或
journal 绑定的恢复点，再取得用户单独同意；安装授权不能代替升级授权。

本地验证：

```bash
go test ./...
go build -o /tmp/memora ./cmd/memora
```

正式 macOS 双架构制品由确定性 Builder 生成，tracked worktree 必须干净：

```bash
./scripts/release.sh 0.1.0 /absolute/output
./scripts/smoke-release.sh /absolute/output 0.1.0
./scripts/publication.sh 0.1.0 /absolute/publication
```

GitHub Release 只由已验证签名的 annotated `vMAJOR.MINOR.PATCH` tag 触发；
workflow 完整测试，并在 arm64/amd64 runner 完成原生冒烟及从 Canonical Skill
开始的零到首条记忆验收后，才上传二进制、checksum、manifest 和带许可证的
Skill bundle。普通 PR 没有发布权限。

## 许可

个人学习、研究、娱乐、兴趣项目及其他非商业用途可依据
[PolyForm Noncommercial 1.0.0](./LICENSE) 免费使用、修改和分发 Memora。
任何商业用途均需事先取得单独的书面付费商业许可证；示例与联系入口见
[商业授权说明](./COMMERCIAL-LICENSE.md)。因此本项目是 source-available，
不是 OSI 定义的开源软件。
