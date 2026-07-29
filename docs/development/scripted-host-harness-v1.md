# Scripted Host Harness v1

状态：F29 已冻结确定性宿主重放格式；不调用真实模型。

## 目的

Harness 用固定 transcript 表达 Codex/Claude Code 已经做出的宿主决策，并
验证这些决策产生的工具调用和数据库结果。它不伪装成语言模型，也不评价
开放式推理；F30–F42 的 Skill 状态机和场景基准都复用这一层。

fixture 版本为 `memora.host-transcript/v1`，包含：

- 用户 turn、脚本 action 和面向用户的 reply；
- 与脚本分离的 `expected_tool_sequence`；
- 每次 Result envelope 的预期状态、错误、行数、revision 或 Row 字段；
- 可选稳定错误注入；
- 重放结束后的真实 MSQL `final_assertions`；
- reply 必含、禁含和字符预算。

所有 JSON 字段严格解析，未知字段直接失败。fixture 中的 MSQL 由当前
Parser 解析；标记为 `query` 的调用只能使用 SHOW、DESCRIBE、SELECT、
MATCH 或 OPEN ROUTE。

## 重放语义

```text
读取并校验 fixture
→ 比较脚本调用与预期工具序列
→ 顺序执行 query/exec，或返回指定的注入 envelope
→ 检查每一步预期
→ 检查用户 reply
→ 执行不参与宿主序列的 final assertions
```

注入调用仍计入宿主尝试序列，但不会调用底层 Tool。这样可确定性测试超时、
revision conflict、权限或截断后的恢复路径。未注入调用通过 Tool 接口交给
真实 Batch Session、CLI/daemon 适配器或测试替身；Harness 本身不绕过
MSQL 修改数据库。

`final_assertions` 必须实际调用 Tool，且不能注入结果。回复断言与数据库
断言分开，避免只验证“说成功了”却没有验证最终逻辑状态。

## 测试边界

F29 只提供确定性播放和断言机制，不决定何时查询、写入或请求用户。具体
Skill 行为由后续 feature 的 transcript fixtures 定义。真实模型只进入受控
smoke/benchmark，不进入普通 CI。

## 关联

- [Canonical Skill v1](../agent/canonical-skill-v1.md)
- [CLI Database Workflow v1](./cli-database-workflow.md)
- [测试约定](./testing.md)
