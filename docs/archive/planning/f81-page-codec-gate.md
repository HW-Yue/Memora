# F81 Page Codec 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：16 KiB Page 可确定编解码并拒绝损坏；
- 用户故事：为后续 RowID 的 B+ Tree 纯代码取数提供可信固定 Page；
- 产品协议影响：无；AI、MSQL、Route Frame 看不到物理 Page；
- 架构选择：CRC32C、64-byte Header、明确 Page Type，见
  [Page Codec v1](../storage/page-codec-v1.md)；
- 永久边界：无 Vector/cosine，无 SQLite，无 Provider，无文件 I/O；
- 明确不做：Page Manager、WAL、Buffer Pool、B+ Tree；
- 用户执行授权：2026-07-31，用户要求执行到 F161；
- 开工前结论：PASS。

## RED 清单

目标命令：

```text
go test ./internal/store/page
```

必须先失败：

- golden Page 尚不能编码；
- Header/Payload 尚不能 round-trip；
- 非 16 KiB、未知类型、flags/reserved、超长 Payload 尚不能稳定拒绝；
- checksum 单 bit 损坏尚不能识别；
- 任意 fuzz seed 尚无“不 panic”保证。

RED 必须因为 codec 返回明确的 not-implemented 错误，不使用编译失败冒充。

实际 RED：

```text
go test ./internal/store/page
FAIL: Encode/Decode returned "page codec is not implemented"
```

失败覆盖 golden、round-trip、invalid value、invalid length、corruption 和 version；
原因是目标能力缺失，不是编译或 fixture 错误。

## 完成证据

- `go test ./internal/store/page`：PASS；
- `go test ./...`：PASS；
- `go test -race ./...`：PASS；
- `go vet ./...`：PASS；
- `FuzzDecodeNeverPanics` 2 秒：25,440 executions，PASS；
- `FuzzRoundTrip` 2 秒：738,668 executions，新增 1 个 interesting input，PASS；
- `./scripts/ci.sh`：format、vet、unit、race、integration、e2e、cross-build 全 PASS；
- `git diff --check`：PASS。

覆盖 Page Type、最大 Payload、golden、字段 round-trip、无切片别名、有效 checksum
下的结构伪造、单字节 corruption 与 unsupported version。

无 MSQL/AI 协议变化，无 Vector/cosine、SQLite、Provider、文件 I/O 或旧路径旁路。
F82 Page File Manager 仍未混入。完成后结论：PASS。
