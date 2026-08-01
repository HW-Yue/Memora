# F111 Route Read Protocol 开工与完成门

状态：已完成，完成门 PASS。

## 唯一主要结果

Admin 可按 point node、单层 children 和 leaf locator 三层有界读取 Router，并安全续页。

## RED

1. `SHOW ROUTES` 只有 backend cursor，没有 list page version/snapshot/limit/input cursor；
2. `OPEN ROUTE` 只能截断，不能返回或接收 continuation cursor；
3. Route/membership 在两页之间变化时会静默重漏；
4. native cursor 未找到时可能从第一页重读，且缺少 canonical/tamper/scope 证据；
5. 协议没有锁定 children/locator 不含 Row 正文。

最小 RED（2026-08-01）：新增 parser 与 native MSQL 契约测试后，`OPEN ROUTE ...
CURSOR` 解析失败，`SHOW ROUTES`/`OPEN ROUTE` 的 `Output.Page` 为空。

## GREEN

- `SHOW` 与 `OPEN` 返回统一 list page；
- shared Route cursor 绑定 scope/snapshot/offset 并 strict decode；
- legacy 与 native backend 对 children/locator 都支持 snapshot continuation；
- node/children/locator 字段白名单和 node-kind 边界固定。

## 不做

- Row detail/history、Change、Trace 或 Admin HTTP；
- Route predictor、向量、ANN 或正文预取；
- 语义树写入/reshape 协议变化。

## 完成门

- [x] parser/parameter/list-page envelope；
- [x] children 与 locator 多页、snapshot conflict、tamper/scope/offset；
- [x] leaf/branch 类型、授权与正文不泄露；
- [x] legacy/native/reopen/race 与完整 CI；
- [x] 独立 commit；合入动作在本文件随 commit 快进到 `main` 时完成。

完成证据：shared Route cursor 覆盖 canonical/tamper/scope/kind/offset/snapshot；legacy
与 native backend 覆盖 children/locator continuation 和 reopen；native MSQL 覆盖 DDL、
membership conflict、类型、授权和字段白名单；真实 daemon e2e 验证 Page authority 下的
node/children/locator envelope。另修复并回归锁定了 backend cursor error 被局部变量遮蔽
后吞掉的问题。`scripts/ci.sh` 全绿。完成后结论：PASS。
