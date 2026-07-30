# Phase C 退出验收

状态：机械宿主兼容证据曾通过；F30 查询旅程和 F42/F51 质量链已失效，因此不再
代表当前产品故事门通过。

## 自动化证据

`tests/integration/phase_c_exit_test.go` 在同一门禁内验证：

- Canonical Skill contract/version/lint 全绿，冲突必须询问用户且临时 View 不入库；
- Codex 与 Claude Code adapter 的 Skill/协议 digest 完全一致；
- F42 固定数据集同时覆盖冷启动接管与宿主切换；
- Skill-first 能力审计无缺口，内置 Runtime 因缺少需求/安全/预算证据 defer；
- v0 Config 不含模型或 key 字段，严格 JSON 拒绝 `api_key`。

具体发现、写入、Schema、会话、冲突、资料覆盖/提交、维护、反馈、安装和 50 轮
场景行为分别由 F30–F42 的 unit、contract、integration 和 e2e 测试覆盖；退出
测试只绑定跨 feature 不变量，不重复实现各模块断言。

## 结论

Codex 和 Claude Code 可通过同一逻辑 Instance、MSQL 和 Result 契约工作；陌生
Agent 不读取旧聊天也有固定冷启动旅程；冲突展示不裁决；用户无需为 Memora
另配模型 API Key。Phase C 可以退出，进入发行与 package 阶段。

这不是 AI-native 发布质量达标声明。F42/F51 包含被禁止的 Vector/cosine 和
非逐层查询路径；必须按产品用户故事重做，原生内核 Feature 当前暂停。

## 复现

```sh
go test -tags=integration ./tests/integration -run PhaseCExit
./scripts/ci.sh
```

## 关联

- [Phase C 计划](./tdd-phase-c-ai-skill.md)
- [AI-native Benchmark v1](../development/ai-native-benchmark-v1.md)
- [ADR-0002](../decisions/0002-defer-embedded-agent.md)
