# Claude Code Adapter v1

状态：F41 实现；F123 升级为 adapter manifest v2 并绑定 Real Host Contract。

## 派生布局

`go run ./cmd/generate-claude-adapter` 从 `skills/memora/` 确定性生成：

```text
adapters/claude-code/
├── .claude/skills/memora/SKILL.md
├── .claude/skills/memora/contract.json
├── .claude/skills/memora/host-contract.json
├── .claude/skills/memora/scripts/install.sh
└── manifest.json
```

Claude Code 从项目或个人 `.claude/skills/<name>/SKILL.md` 发现 Skill，用户可
显式 `/memora` 调用，description 也允许自动触发。适配层保留 Canonical Skill
正文逐字一致，只在 YAML frontmatter 增加宿主权限元数据。

## 权限差异

`allowed-tools` 精确列出当前九个 `Bash(memora <command> *)`，授权仅在 Skill
被调用的当前 turn 生效。它不是工具白名单，也不能覆盖用户、项目或 managed
settings 中更高优先级的 deny/ask。适配层不使用 `Bash(memora *)`，未来命令
默认不继承权限；install.sh、init 和 daemon 仍走正常审批及 F39 显式授权。

## 同源兼容

Codex 与 Claude Code v2 manifest 都记录 canonical Skill、protocol contract 和
Task contract digest。e2e 要求三者完全相同；宿主包装不得改变 MSQL、Result、
Mutation Policy、冲突边界、数据内容或预期 revision。两端共享同一 Scripted
Host 核心旅程，宿主差异只允许存在于发现目录、调用名和权限表达。

## 官方规范依据

- [Claude Code Skills](https://code.claude.com/docs/en/slash-commands)
- [Claude Code Permissions](https://code.claude.com/docs/en/permissions)

## 关联

- [Canonical Skill v1](../agent/canonical-skill-v1.md)
- [Codex Adapter v1](./codex-adapter-v1.md)
- [Skill 首次安全安装 v1](../agent/safe-bootstrap-v1.md)
