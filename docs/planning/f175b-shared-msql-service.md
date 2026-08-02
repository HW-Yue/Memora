# F175b：单实例共享 MSQL Service

规划状态：已完成；RED → GREEN → REFACTOR 与完整 CI 通过。

## 唯一主要结果

建立一个实例级 `internal/msql/service.Service`：IPC handler 与未来同进程 Agent adapter 从它创建
独立逻辑 Session，并最终调用同一个 `executor.BatchSession` 路径。F175b 不建立 Agent package，
不实现 Bootstrap、Provider 或模型 loop。

## 生命周期与并发

- 一个打开的 Instance 只组装一个 Service，Service 持有共享 Catalog/Rows/Page Authority 等依赖；
- 每个 IPC connection 或 Agent run 使用唯一 session_id，Session 内请求严格串行，不同 Session 可并发；
- Session close 先取消 active request，再等待执行边界并回滚未完成事务；Service close 原子拒绝新 Session，
  取消并关闭全部现存 Session；
- request context 取消/超时只终止对应调用，不关闭 Session；session/service 取消必须传播到 active 调用；
- 同一 Session 的排队请求可以在取得执行权前响应取消，不能被前一条慢请求无限拖住。

## 两个 adapter、一个执行核心

- `Session.ExecuteMSQL(protocol/msql.Request)` 是不可信同进程入口：先执行 protocol Validate，再转换为内部输入；
- IPC `msql.execute` 保持原 payload，并调用同一 Session 的 batch core；无 StatementInput 的可信本地 CLI
  兼容模式继续存在，不能通过 Agent adapter 触发；
- protocol/internal DTO 只在 Service 边界显式转换；不使用反射、JSON round-trip 或 database fast path；
- 同一授权请求经 IPC 与同进程 adapter 的 Statement Results 必须 JSON 等价，差异只允许 request_id。

## 完成证据

- in-process MSQL 能执行真实 Catalog/Row 旅程；
- 两 Session 的事务状态隔离，close 自动 rollback；
- active、queued request 的取消/超时与 Service close 均有确定性测试；
- 两 Session 并发 expected revision 写入只有一个成功，race 全绿；
- daemon IPC parity、断线回滚和旧 CLI/SDK wire 测试保持通过；
- 完整 unit/race/vet/integration/e2e/cross-build。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F175b 范围。

开工前结论：PASS。

## 完成结论

- `internal/msql/service` 已成为实例级依赖与 Session 生命周期所有者；daemon 不再维护第二套 Session map；
- `ExecuteMSQL` 对中立协议执行验证和显式 DTO 转换，`ExecuteBatch` 仅保留本地 IPC/旧内部工作流兼容；
- 可取消 gate 保证同 Session 串行，同时让 queued call 独立响应取消；close 先取消 active call 再回滚；
- 已验证真实 Row 写入、事务隔离、断线回滚、expected-revision one-winner、IPC parity 和旧 SDK wire；
- `format`、`vet`、全量 unit/race、integration、e2e 与独立 cross-build 全部通过。

完成结论：PASS。下一项为 F175c Agent MSQL-only port 与 fake harness。
