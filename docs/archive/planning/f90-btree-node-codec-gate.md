# F90 B+ Tree Node Codec 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：internal/leaf Node 可确定编码到 16 KiB Page 并严格拒绝损坏；
- 依赖：F81 Page Codec 已完成；
- 格式：见 [B+ Tree Node Codec v1](../../storage/btree-node-codec-v1.md)；
- 明确不做：search、cursor、mutation、split、root persistence；
- 用户执行授权：2026-07-31，用户要求执行到 F161；
- 开工前结论：PASS。

## RED

`go test ./internal/store/btree` 必须因 Node Codec 未实现而失败：

- leaf/internal golden payload 与 round-trip；
- empty leaf、internal zero separator、Page 容量边界；
- key/value/child 深复制隔离；
- wrong Page type/kind/level、空 key/value、zero child、乱序/duplicate 拒绝；
- header/slot reserved、free bytes、offset/length/key length 越界拒绝；
- Slot overlap、Record gap/overlap、非紧密尾部拒绝；
- bit-flip/seed corpus 不得 panic，错误必须稳定归类。

RED 已确认：首次运行 `go test ./internal/store/btree` 时，全部用例均因明确的
`B+ Tree Node Codec is not implemented` 失败，而非编译或 fixture 错误。

## 完成门

- leaf header/Slot/record golden、leaf/internal/empty round-trip：PASS；
- 精确 16320-byte payload 容量边界与 over-capacity：PASS；
- header/Slot/free/offset/length/key/order corruption corpus：PASS；
- 外层 Page CRC bit-flip seed corpus：PASS；
- key/value 双边界深复制，Decode→Encode byte-identical：PASS；
- `go test -count=20 ./internal/store/btree`、`go test -race ./internal/store/btree`：PASS；
- `go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build：PASS；
- 不包含 F91 Point Search：PASS。

完成门结论：PASS。
