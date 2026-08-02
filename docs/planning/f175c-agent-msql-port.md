# F175c：Agent MSQL-only Port 与 Fake Harness

规划状态：已通过单项 Review，批准按 RED → GREEN → REFACTOR 实现。

## 唯一主要结果

建立 `internal/agent` 的消费者侧 `MSQLExecutor` port、显式构造注入和可复用 scripted fake，
并用 CI import allowlist 将 Agent 对 Memora 的访问永久限制为 `protocol/msql`。

F175c 不实现 Query Bootstrap、Provider、模型 loop、Trace、资料解析或 daemon 产品入口；这些分别属于
F176 以后独立 Feature。当前 Runtime 只负责验证版本化请求与结果并调用注入端口一次，不重试、不改写
MSQL、不打开 Instance。

## 依赖契约

- `internal/agent` 生产代码只允许标准库与 `protocol/msql`；
- Agent 测试只额外允许 `internal/agent/*` 自身测试工具，不能导入 SDK、daemon、MSQL Service 或内核；
- 接口由 Agent 包定义，具体 Session adapter 只在未来 composition root 组装；
- 构造函数拒绝 nil executor，不使用全局变量、注册表、反射 DI 或隐藏默认实现；
- outbound Request 在调用 fake/adapter 前 Validate，inbound Envelope 返回调用方前 Validate；
- executor error 与 context 取消原样返回，不在边界自动重试。

## Fake Harness

`internal/agent/agenttest` 提供并发安全的 scripted MSQL fake：逐次校验完整 Request、返回预设
Envelope/error、记录调用并在结束时验证没有漏调或多调。它不模拟 Parser、Policy 或数据库行为。

## 完成证据

- nil dependency、invalid Request、invalid Envelope 和 executor error 单元测试；
- 精确 Request capture、单次调用和无自动重试测试；
- scripted fake 的顺序、漏调、多调和并发安全测试；
- CI AST import allowlist 覆盖 `internal/agent/**/*.go`，包括测试文件；
- Agent 测试不创建文件、不打开 Store/Instance，完整 unit/race/vet/CI 全绿。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F175c 范围。

开工前结论：PASS。
