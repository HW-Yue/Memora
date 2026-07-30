# Codex Adapter v1

状态：F40 实现规格，已冻结。

## 派生布局

`skills/memora/` 仍是唯一行为来源。`go run ./cmd/generate-codex-adapter`
确定性生成 `adapters/codex/`：

```text
.agents/skills/memora/SKILL.md
.agents/skills/memora/contract.json
.agents/skills/memora/scripts/install.sh
.agents/skills/memora/agents/openai.yaml
.codex/rules/memora.rules
manifest.json
```

Codex 按官方本地 Skill 发现规则读取 `.agents/skills`。生成器复制而不改写
Canonical Skill/contract/bootstrap，manifest 记录每个文件 SHA-256 和 mode；CI
比较 checked-in bundle 与生成结果，防止宿主包装漂移。

## 触发与权限

`agents/openai.yaml` 允许隐式调用，具体匹配仍由 Canonical Skill frontmatter 的
name/description 决定；用户也可显式 `$memora` 调用。适配层不复制另一份行为
prompt，不假设会话结束 hook，也不改变 MSQL 或 Result 版本。

受信任项目可加载 `.codex/rules/memora.rules`。规则只 allow 当前九个稳定逻辑
子命令；不使用宽泛 `pattern=["memora"]`，因此未来命令不会静默获得权限。
`init`、`daemon` 和 `/bin/sh install.sh --yes` 不在 allow 规则内，首次安装仍必须
由 Codex 权限系统和 F39 的用户授权共同放行。

## 验收

Scripted Host e2e 在临时 Codex 根目录安装 bundle，并通过真实 MSQL Session
覆盖：Schema 发现、重复检查、自动写入、回表查询、expected revision 修订、
History 读取和冲突并列展示。冲突回合不执行 mutation，回复必须包含 Row ID、
revision 和请求用户选择。

## 关联

- [Canonical Skill v1](../agent/canonical-skill-v1.md)
- [Skill 首次安全安装 v1](../agent/safe-bootstrap-v1.md)
- [Scripted Host Harness v1](./scripted-host-harness-v1.md)
