# F80 发行运行时机械故事门

状态：已实现；F127 起明确降级为 AI Story Gate 的 mechanical prerequisite，不能单独
产生产品级 AI PASS。

## 门禁对象

`internal/storygate.Run` 接收一个已构建 `memora` 二进制、Canonical Skill、宿主名和
全新目录。Codex 与 Claude 各自安装 adapter、创建独立 Instance，并只通过公开
CLI/MSQL 执行旅程。两个报告必须绑定同一 binary SHA-256、canonical digest 和
protocol digest。

```text
干净 adapter + 隔离 Instance
→ Schema / Table Router / INSERT
→ 顶层 Route → 子层 → locator → RowID SELECT
→ UPDATE / DELETE / RESTORE，每次从顶层重查
→ SPLIT / MERGE，每次从顶层重查
→ source inventory / coverage / challenge-bound review / Source Receipt
→ feedback / semantic health / 配置 revision 与补偿恢复
→ daemon restart / 顶层 Route 重查 / doctor
```

F123 起 `memora.real-story-gate/v2` 报告还绑定 Real Host Task contract digest；报告
逐步保存公开命令输出 SHA-256，并覆盖全部 16 个
`US-*`。INSERT、UPDATE、DELETE、CORRECT、OPTIMIZE、SPLIT、ASSIMILATE、
RECOVER 和 ENGINE 缺少 `rediscovered_from_root` 时报告无效。

## 双 adapter 边界

门禁使用干净目录和预写 MSQL 旅程验证 Codex/Claude adapter 和统一协议，不调用任何
模型 Provider，也不再称为真实 AI transcript。模型地址与 Key 继续由宿主管理；
OpenAI-compatible 可以指向 Kimi 等自定义地址，Memora 不接收这些凭证。

## 执行

仓库 E2E 会先构建当前二进制，再同时验证两份报告：

```sh
go test -tags=e2e ./tests/e2e \
  -run TestReleaseBinaryPassesRealCodexAndClaudeStoryJourneys
```

也可对指定发行二进制单独运行 `go run ./cmd/run-story-gate`，显式提供 binary、
data-dir、canonical-skill、adapter-dir 和 host。Codex 与 Claude 都通过后只满足
mechanical prerequisite；产品级 PASS 还需 F127 的 32 份真实 AI journey evidence。

## 不宣称的范围

此门证明公开二进制的逻辑命令闭环接通，不证明模型自主判断、语义质量或用户故事成立。
新增机械能力需扩充运行时旅程；新增 AI 声明还必须扩充 F127 Suite 与真实 evidence。
