# F179 Runtime Spike

这里的嵌套 Go module 只用于复现 F179 ADR 数据，不属于 Memora 生产依赖。

二者执行相同的 `bootstrap → provider → tool → checkpoint → provider → final` 固定工作负载，
并验证 `context.Context` 取消。`thin` 使用显式状态机，`eino` 使用 Eino Compose Graph。

从各自目录运行：

```sh
go test ./...
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o ../_out/<candidate> .
/usr/bin/time -l ../_out/<candidate>
go list -deps .
go list -m all
```

`_out/` 是本地测量产物，不提交。

`memora-eino` 额外用当前 `cmd/memora` 等价入口保留一个可达 Eino Graph，用来测量在现有
Memora 二进制上叠加 Compose 的体积，而不是把两个最小程序的比例误当成产品增幅。
