# Memora

Memora 是由 AI 自主建模、通过版本化 MSQL 读写的本地个人数据库。

项目当前处于端到端原型与发行准备阶段。本地 `memora` daemon 长期承载数据库 Instance 和统一 MSQL 执行引擎；Codex/Claude Code 按 Memora Skill 通过 CLI 连接 daemon：

```text
memora --stdio        为外部 Agent 提供长驻 JSONL 会话
memora exec <msql>    直接执行 MSQL
memora daemon         管理本地常驻服务
```

所有 Agent 操作必须经过同一套 MSQL Parser、Policy、事务和执行器；Agent 不能直接操作 Page、索引或日志。自带模型的 `memora ask` 属于 v0 发布后的可选评估，不阻塞 Skill-first 产品。

当前设计入口见 [`docs/README.md`](./docs/README.md)。

## 当前实现状态

开发按 [TDD 开发总计划](./docs/planning/tdd-development-plan.md) 推进。当前 CLI 提供 Instance/daemon、MSQL query/exec、Skill 写入与 Schema 计划、资料吸收、Database Package、Wiki 导出和诊断链路。基础入口包括：

```text
memora help
memora init
memora init --data-dir /absolute/path
memora init --instance work --log-level debug
memora daemon start --data-dir /absolute/path
memora daemon status --data-dir /absolute/path
memora daemon stop --data-dir /absolute/path
memora version
memora version --json
```

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
workflow 完整测试并在 arm64/amd64 runner 冒烟后，才上传二进制、checksum、
manifest 和带许可证的 Skill bundle。普通 PR 没有发布权限。

## 许可

个人学习、研究、娱乐、兴趣项目及其他非商业用途可依据
[PolyForm Noncommercial 1.0.0](./LICENSE) 免费使用、修改和分发 Memora。
任何商业用途均需事先取得单独的书面付费商业许可证；示例与联系入口见
[商业授权说明](./COMMERCIAL-LICENSE.md)。因此本项目是 source-available，
不是 OSI 定义的开源软件。
