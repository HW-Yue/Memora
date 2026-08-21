# F97c2 Root/Allocator Redo Codec 开工与完成门

状态：完成，PASS；由 F97c2 规模 Review 拆出。

## 唯一结果

`root`/`allocator` redo payload 可确定编解码，并拒绝非法 generation、root、
high-water、retired Page 和未知版本。

## 产品门

- 用户故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；
- 依赖：F83 WAL Record Stream、F97c1 Tree Control Codec；
- 规格：[Root/Allocator Redo v1](../storage/root-allocator-redo-v1.md)；
- 明确不做：Page recovery、生成 Tree mutation、运行时 durable commit、业务索引、
  MVCC 和 Page 复用。

结论：PASS。Payload codec 与 Page recovery 是独立协议/故障域；原 F97c2 因生产代码
超过约 400 行在合入前拆为 F97c2/F97c3。

## RED Matrix

- root expected/next generation、expected/next root 与 expected high-water round-trip；
- allocator expected/next high-water、有序 retired Page round-trip 与深复制；
- generation overflow/skip、bad root/high-water、retired 乱序/重复/越界拒绝；
- bad magic/header/count/reserved、unknown version 与 payload 长度拒绝。

## 完成门

- `go test -count=20 ./internal/store/wal -run '^TestTreeRedoCodec'`；
- `go test ./...`、`go test -race ./...`、`go vet ./...`；
- `gofmt`、`git diff --check` 与 `./scripts/ci.sh`；
- 完成证据、计划状态和独立原子 commit 同步。

## 完成证据

- Root/Allocator golden header、round-trip 与 retired slice 深复制：PASS；
- generation overflow/skip、root/high-water、retired 乱序/重复/越界：全部拒绝；
- magic、header size、reserved、count、truncate 与 unknown version：全部拒绝；
- `go test -count=20 ./internal/store/wal -run '^TestTreeRedoCodec'`：PASS；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build：PASS。
