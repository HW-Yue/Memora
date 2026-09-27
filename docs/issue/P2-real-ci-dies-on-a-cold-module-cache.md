# P2 · 真实 CI 挂在第一关：冷模块缓存下 `go generate` 拿到空路径

严重度：high（门是红的，等于没有门）。状态：**已关**（2026-09-26 复现，2026-09-27 修复并经真实 CI 验证）。

## 结案

**不是头文件过期，是指令依赖了一个偶然。** 核对过：检入的
`internal/sqlstore/vecext/include/sqlite3.h` 与 driver `v1.14.32` 本来就一致（本机跑
`go generate` 后 `git diff` 为空）。

真正的原因：`go-sqlite3` 是在 build tag 后面被 import 的，所以这个包自己的构建**不需要**它的文件；
冷缓存里 `go list -m -f '{{.Dir}}'` 于是安静地返回空串（exit 0），`cp` 拼出 `/sqlite3-binding.h`。
本机永远看不出来——缓存早就解出过这个模块；而 CI 的 `format` 跑在任何构建之前，所以每次都挂。

修法：generate 指令先 `go mod download github.com/mattn/go-sqlite3`，让 `{{.Dir}}` 真实存在。

- 修复提交 `237bb066`（merge `65ad5f34`）。
- 新增**离线可复现**的回归 gate `TestVecextHeaderRefreshSurvivesAColdModuleCache`
  （模块缓存里只放 download cache、不放解出的模块目录），先 RED 复现 CI 同一句报错，再 GREEN。
- 真实 CI：run `36326905880` **全绿**（5m22s，macos-latest）。
- 本地 `./scripts/ci.sh` 六个 stage 全绿。

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

## 当时的候选修法（已选第 1 条）

1. **采用**：让指令自己先 `go mod download github.com/mattn/go-sqlite3` —— 修在指令里，
   `go generate` 冷热都自足，不依赖谁先跑过构建；
2. 未采用：把指令改成对**包**目录做 `go list -f '{{.Dir}}'`（冷缓存下它会自己下载，行为上也可行，
   但语义上要的是"模块目录"，靠"这个包的目录恰好等于模块根"是巧合）；
3. 未采用：在 ci.sh 或 CI setup 里显式 `go mod download`（能修 CI，但开发者直接跑
   `go generate` 仍会踩同一个坑）。

保留住了这条指令的本意：**driver 升级后忘了重新生成时，门必须红** —— 没有用"跳过这一步"来修。

## 这条为什么记在 issue 而不是计划里

它不是"还没排期的设计"，是一扇**现在就是红的门**——按 `AGENTS.md`，门红等于这期间所有合入都没有背书。
