# P2 · 真实 CI 挂在第一关：冷模块缓存下 `go generate` 拿到空路径

严重度：high（门是红的，等于没有门）。状态：**待做**（2026-09-26 复现）。

## 症状

`main` 上的真实 CI 在第一个 stage 就失败：

```
ci: format
cp: /sqlite3-binding.h: No such file or directory
internal/sqlstore/vecext/generate.go:9: running "sh": exit status 1
```

失败发生在 `scripts/ci.sh` 的 `format` 阶段，`go generate ./internal/sqlstore/vecext/` 里。
因为 `main` 归位（2026-09-26）之前真实 CI 只在 PR 上跑，主线长期没有门；一推 `main` 就露出来了。

## 证据

- 运行记录：`CI` / `main` / push，`36323763732`（`9f44ca53`，47s 失败）与 `36324033698`（`5815f0d9`，45s 失败）。
- `internal/sqlstore/vecext/generate.go:9`：
  `//go:generate sh -c "cp \"$(go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)/sqlite3-binding.h\" include/sqlite3.h"`
- 复现（本机、空缓存即模拟冷 runner）：

  ```sh
  GOMODCACHE=/tmp/mc-empty GOCACHE=/tmp/gc-empty \
    go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3
  # 输出空行；替换进 cp 就是 "/sqlite3-binding.h"
  ```

  本机之所以能过，是因为本机的模块缓存里早就有这个模块（`{{.Dir}}` 非空）。
- 判断：`go list -m` **不会**为了取 `.Dir` 去下载模块本体，而 `vecext` 对 go-sqlite3 的 import
  在 build tag 后面，`go generate`（不带 tag）时该模块的包并未被加载 → 冷缓存下 `.Dir` 为空。

## 候选修法（**未验证**，别当已定结论）

1. `format` 阶段在 `go generate` 之前先 `go mod download github.com/mattn/go-sqlite3`；
2. 或把指令改成会强制加载包的形式（例如带 `sqlite_fts5` tag 对**包**目录做 `go list -f '{{.Dir}}'`）；
3. 或在 CI 的 setup 步骤里显式 `go mod download`（setup-go 的 `cache: true` 只缓存，不保证下载）。

修法要保留这条指令的本意：**driver 升级后忘了重新生成时，门必须红**。所以别用"跳过这一步"来修。

## 这条为什么记在 issue 而不是计划里

它不是"还没排期的设计"，是一扇**现在就是红的门**——按 `AGENTS.md`，门红等于这期间所有合入都没有背书。
