# 测试约定

状态：2026-09-20 按 SQLite 基座修订门禁。

## 默认命令

```bash
./scripts/ci.sh
```

它按固定顺序执行 format、vet、lint、unit、race，以及带 CGO 的本机构建并打开数据库。
开发中可以只运行一层：

```bash
./scripts/ci.sh --list
./scripts/ci.sh --stage unit
./scripts/ci.sh --stage cgo-build
```

`gofmt` 只扫 `cmd/` 和 `internal/`。没有独立的 `tests/` 目录，也没有
`//go:build integration|e2e` 文件，所以没有空转的 tag stage。

构建必须 `CGO_ENABLED=1`。`go-sqlite3` 在 `CGO_ENABLED=0` 时链到 `static_mock.go`，
产物打不开数据库；`cgo-build` 拒绝这条路径，并在本机跑 `init` / `daemon` / `doctor`。
交叉编译 sqlite3 需要 C 交叉编译器，本仓库不提供，本机 runner 各验各的三元组。

普通测试不得访问网络、真实用户 datadir 或模型 API。

GitHub Actions 与本地开发调用同一个 `scripts/ci.sh`，不得在 workflow 中复制另一套测试顺序。PR CI 只有 `contents: read` 权限，不发布 Release。签名发布工具链尚未移植到 SQLite 基座；对 `v*` tag 的 Release workflow 会明确失败，而不是调用不存在的脚本。

## TDD 证据

每个 feature 在本地先观察目标测试因缺少行为而失败，再写最小实现。最终合入的单一 commit 同时包含测试、实现和必要文档，并保持所有门禁为绿；不向 `main` 提交故意失败的 RED 状态。

## 隔离规则

- 文件测试只使用 `t.TempDir()`；
- 环境变量修改不得与 `t.Parallel()` 混用；
- fixture 不依赖执行顺序或上一个测试留下的状态；
- 测试结束后的清理由 Go testing 生命周期负责；
- 故障注入必须命名并可计数，不能用随机 sleep 模拟竞态。
