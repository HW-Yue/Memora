# 测试约定

状态：F02 起执行。

## 默认命令

```bash
./scripts/ci.sh
```

它按固定顺序执行 format、vet、unit、race、integration、e2e 和无 CGO macOS 双架构 cross-build。开发中可以只运行一层：

```bash
./scripts/ci.sh --list
./scripts/ci.sh --stage unit
./scripts/ci.sh --stage integration
```

普通测试不得访问网络、真实用户 datadir 或模型 API。真实 Codex/Claude 测试属于受控 smoke/benchmark，不进入默认 PR 门禁。

GitHub Actions 与本地开发调用同一个 `scripts/ci.sh`，不得在 workflow 中复制另一套测试顺序。PR CI 只有 `contents: read` 权限，不发布 Release 或上传产品制品；发布流程属于后续独立 feature。

## Testkit

`internal/testkit` 提供：

- `Sandbox`：每个测试独立临时根目录，拒绝绝对路径、`..` 和符号链接逃逸；
- `FakeClock`：确定性读取与推进时间；
- `FakeIDs`：按固定顺序产生 ID，耗尽后明确失败；
- `Faults`：在命名 fault point 的第 N 次命中时注入错误；
- `CompareGolden`：显式比较或更新 golden；
- `ReadFixture`：只从调用测试旁边的 `testdata/` 读取 fixture。

生产代码中的时间、ID 和故障点必须通过窄接口注入，不能在核心测试中依赖真实时钟、随机 ID 或进程运气。

## TDD 证据

每个 feature 在本地先观察目标测试因缺少行为而失败，再写最小实现。最终合入的单一 commit 同时包含测试、实现和必要文档，并保持所有门禁为绿；不向 `main` 提交故意失败的 RED 状态。

## 隔离规则

- 文件测试只使用 `t.TempDir()`/`testkit.Sandbox`；
- 环境变量修改不得与 `t.Parallel()` 混用；
- fixture 不依赖执行顺序或上一个测试留下的状态；
- 测试结束后的清理由 Go testing 生命周期负责；
- 故障注入必须命名并可计数，不能用随机 sleep 模拟竞态。
