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

任何 `go build` / `go test` / `go vet` 都必须带 `-tags sqlite_fts5`：FTS5 是可选模块，
不带标签构建出来的二进制里**召回没有索引**，模块可用性测试会当场失败（`./scripts/ci.sh`
已经统一带上了）。下游若用 `go test ./...` 裸跑，记得自己加标签。

构建必须 `CGO_ENABLED=1`。`go-sqlite3` 在 `CGO_ENABLED=0` 时链到 `static_mock.go`，
产物打不开数据库；`cgo-build` 拒绝这条路径，并在本机跑 `init` / `daemon` / `doctor`。
交叉编译 sqlite3 需要 C 交叉编译器，本仓库不提供，本机 runner 各验各的三元组。

普通测试不得访问网络、真实用户 datadir 或模型 API。

要**真的调用模型**时，用可选、不进门禁的工具，而不是放宽这条：`scripts/demo-recall.sh`
读宿主环境（或 `~/.zshrc` 里的赋值，绝不回显）里的 `MEMORA_EMBEDDING_*`，建一套语义树、
用真 provider 算向量，走一遍逐层导航与三条召回，最后**把 provider 指向一个死端口**再写一条，
证明"写入照常、向量留成待办、下一次正常写入把欠账一起补上"。它按定义会联网，所以只有显式
运行才会跑。

GitHub Actions 与本地开发调用同一个 `scripts/ci.sh`，不得在 workflow 中复制另一套测试顺序。PR CI 只有 `contents: read` 权限，不发布 Release。签名发布工具链（`publication.sh`／`verify-publication`／`smoke-release`／`clean-machine-acceptance`）尚未移植到 SQLite 基座；Release workflow 只做它能诚实做到的部分：打 `v*` tag → 在 macOS 上**先跑完整门禁** → 构建 darwin/arm64 与 darwin/amd64 → 按 `install.sh` 校验的布局打包 + `checksums.txt` → 用 `gh` 发布。**不引任何第三方 action**，也不调用不存在的脚本。发布件**未签名未公证**，那条缺口仍在。

## 不变量守门

测试实例一律以 `sqlstore.Options{CheckInvariants: true}` 打开：每次写入提交前断言
「live 行**恰好**被一个活跃叶子指向」，三类违例（零叶子行、多叶行、叶子不回指）任一非零
就回滚并报 `internal_error`。断言只有这一处，落在 autocommit 与显式事务共用的提交点上。

生产实例不开这个开关，靠 `memora doctor` 报同样三条查询——同一份 `mountViolations`，
所以运维看到的和写入被要求的是同一件事。新路径写歪了会当场炸在它自己的提交上，而不是
拖到别人想起来跑 doctor 时才发现。

## 信封必须可投递

测试夹具拿到 `ExecuteBatch` 的结构体后**必须再序列化一次**（`requireDeliverable`）：
信封校验只在序列化时跑，进程内拿到 `StatementResult` 是看不出问题的。`SHOW ARCHIVE`
就因此带着一个空的必需页字段上线了——它的测试全绿，而真实调用每次都在客户端报
`invalid list page metadata`。直连结构体的断言证明不了读面可用。

## cgo 那一半要在 Linux 上跑

宿主门禁证明不了 cgo 的跨平台：`scripts/ci.sh` 里两个 `GOOS` 的 vet/lint 都以
`CGO_ENABLED=0` 跑（走 `!cgo` 兜底文件），**真正编译 C 的只有 unit/race/cgo-build，
而它们编的是本机平台**。sqlite-vec 的跨平台失败就是这样漏出来的：macOS 碰巧从系统 SDK
拿到 `sqlite3.h`，Linux 容器没有，构建直接死在头文件上。

本地用 `scripts/ci-linux.sh`（Docker，跑 Linux 上的 cgo 构建 + 开真库的测试）补这一半。
**CI 只跑 macOS**（见 `.github/workflows/ci.yml`）：Linux 不再是我们发布的目标平台，
所以它只在 `scripts/ci.sh` 的 vet/lint 里被扫（`GOOS=linux` 的编译检查，`CGO_ENABLED=0`）。
Linux 的那一半 cgo 现在**只有**这个脚本能验，它是可选工具，不进门禁。

`internal/sqlstore/vecext/include/sqlite3.h` 是**生成物**（`go generate ./internal/sqlstore/vecext/`，
从驱动复制），不要手改：它必须与所链的 SQLite 库同版本，否则 sqlite-vec 是拿另一套声明去
编这个库。`format` stage 会重跑生成器并 `git diff --exit-code`，驱动升级漏了这一步就当场红。

## 一个 fixture，四种走法

`internal/sqlstore/semantic_tree_test.go` 是门禁里的那棵树：与 `scripts/demo-recall.sh` 同形状
（三个域、一叶一事实），但用**固定向量**而不是 provider，所以"最近的"是数字的性质而不是模型
的心情。逐层导航／关键词／向量／并集四条路都跑同一棵树——它们是通往同一个地方（一个位置）的
四种方式，同树跑才有对照：改动任何一条，另外三条会替你说话。

`LIMIT` 在并集里会截断**合并后**的列表，所以小 limit 证明不了"并集更宽"。这个 fixture 因此在
满宽度下断言"每个位置恰好一次"（含两路都找到的那个），而不是断言顺序。

## TDD 证据

每个 feature 在本地先观察目标测试因缺少行为而失败，再写最小实现。最终合入的单一 commit 同时包含测试、实现和必要文档，并保持所有门禁为绿；不向主线分支提交故意失败的 RED 状态。

## 隔离规则

- 文件测试只使用 `t.TempDir()`；
- 环境变量修改不得与 `t.Parallel()` 混用；
- fixture 不依赖执行顺序或上一个测试留下的状态；
- 测试结束后的清理由 Go testing 生命周期负责；
- 故障注入必须命名并可计数，不能用随机 sleep 模拟竞态。
