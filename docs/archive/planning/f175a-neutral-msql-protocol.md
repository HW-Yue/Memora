# F175a：中立 MSQL Wire Protocol

规划状态：已完成；2026-08-03 单项 Review、RED → GREEN → REFACTOR 与完整 CI 均通过。

## 唯一主要结果

新增只依赖 Go 标准库的 `protocol/msql`，成为 SDK、未来共享 MSQL Service 和内置 Agent 共用的
版本化 Request/Envelope 类型源。F175a 不改 daemon 执行路径、不增加 Agent，也不统一 Session。

## 兼容策略

- `RequestVersion` 首版继续是既有 `memora.go-sdk-request/v1`；字符串改名属于新 wire version，不能借重构偷改；
- SDK 公共类型与常量改为 protocol aliases，现有源码调用和方法集合保持兼容；
- SDK 发给现有 `msql.execute` 的 payload 仍只有 `source + statements`，不新增 version 字段；
- Envelope JSON、`UseNumber` 行为、`RawJSON()` 防御性副本和现有校验结果保持不变；
- `row_detail` 与 `discovery` 在中立 Envelope 中继续是 `json.RawMessage`，避免 protocol 依赖引擎领域包。

## 依赖边界

`protocol/msql` 包含 Request、StatementInput、Parameters、Mutation、Authorization/Approval、Envelope、
StatementResult 和必要版本/枚举/Validate。生产文件只能 import 标准库，禁止 import `internal/*`、
SDK、daemon、IPC、Catalog、Row、Router、result 或 Agent。

数据库内部的 `executor`/`result` 类型迁移与唯一执行 Service 分别由 F175b 处理；F175a 只建立不会
形成依赖环的公共 wire 叶子包。

## RED 与完成门

- protocol request/envelope validation 与 RawJSON 单测先失败；
- strict import test 锁定生产文件只能依赖标准库；
- SDK compile-time alias 与 request payload / response envelope JSON golden；
- `go test ./protocol/msql ./sdk/memora`、全量 unit/race/vet/integration/e2e/cross-build；
- 完成后更新 SDK、Agent 边界、Feature 状态和后续计划。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F175a 范围。

开工前结论：PASS。

## 完成证据

- `protocol/msql` 已成为 Request/Envelope、Authorization、Mutation 和版本常量的唯一实现；
- 生产文件静态测试确认只 import Go 标准库；
- `sdk/memora` 使用类型/常量/error aliases，既有方法集合和 `errors.Is` identity 保持不变；
- request payload golden 字节未变化，Envelope 继续保留 `json.Number` 和防御性 `RawJSON()`；
- 显式空 Route membership 仍编码为 `[]`，没有被 `omitempty` 吞掉；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e 全绿；cross-build 独立复跑全绿。

完成门结论：PASS。F175b 可以让 IPC 与同进程 adapter 共用一个版本化 MSQL Service。
