# F133 Host Input Capture 开工门

状态：已完成（2026-08-01）。

## 单一主要结果

宿主把一条有界候选输入提交到 Memora 的临时 auxiliary inbox，并获得可跨重试、重启
恢复且不回显正文的稳定 receipt；该候选尚不是 Database 事实。

## 产品门

- 协议为 `memora.host-input/v1` 与 `memora.host-input-receipt/v1`；
- 每条只接受一个 UTF-8 candidate text，最大 12,000 bytes；完整文档/目录仍走 assimilation；
- 输入绑定稳定 input ID、workspace、actor、1–32 个授权 Database selector 与来源级别；
- conversation assertion 不伪造 locator/hash；document/repository anchor 必须同时携带短
  locator 与 source SHA-256；reviewed 不能由 capture 自封；
- receipt 只返回 input/content/scope hash、字节数、状态和 capture time，不回显正文；
- 同 ID 同 canonical 内容重试返回 replay；同 ID 异内容返回 revision conflict；
- 可按 input ID + workspace 重新读取 pending candidate，跨 daemon restart 不依赖旧会话；
- pending inbox 有硬上限，不写 Catalog/Row/Route/History/Change Log。

## RED 清单

1. 尚无 Host Input 版本模型、严格校验、canonical digest 或稳定 receipt；
2. raw candidate 可借 title/locator/scope 绕过长度与来源边界；
3. 同 ID 异内容覆盖，或同内容重试重复写入；
4. workspace 不匹配仍可读取候选；
5. pending 数量无界；
6. CLI/IPC/daemon 无 capture/retrieve 入口或接受未知字段；
7. daemon restart 后 receipt/candidate 丢失；
8. Capture 意外触发逻辑数据库写入，或被描述成 worthiness 决策。

## 明确不做

- 不判断 ignore/write/revise；该决策属于 F134；
- 不自动发现 Database/Table、建 Schema、写 Row 或改 Route；
- 不接收二进制、附件、chunk 数组、Embedding 或完整长文档；
- 不在 F133 清除或归档已决候选，生命周期转移由 F134 设计。

## 完成门

reference、strict decode、idempotency/conflict、capacity、workspace isolation、CLI/daemon
reopen 与 no-database-write 证据全绿；targeted race 和全仓 CI 通过后进入 F134。

## 完成证据

- `internal/hostinput` 覆盖稳定 receipt、并发幂等、revision conflict、来源校验、容量上限、
  workspace isolation、损坏记录拒绝和 reopen；
- daemon 旅程证明 capture 前后 `SHOW DATABASES` 仍为空，重启后可重载并 replay；
- strict IPC/CLI 拒绝未知字段，审计事件不包含 candidate text；
- Canonical Skill 校验和 Codex/Claude adapter 生成测试通过；
- `go test -race ./internal/hostinput ./internal/daemon ./internal/cli` 与 `./scripts/ci.sh`
  全绿。

下一项：F134 Worthiness Decision。
