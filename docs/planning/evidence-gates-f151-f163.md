# F151–F163 按证据触发门

状态：执行中；每个门先冻结条件与 workload，再记录结果。未越门槛只代表当前延后，
不是永久取消。

## F151 Compaction

状态：已评估，进入条件未成立，延后。

- 冻结门槛：固定宽度的 256 条 current-row locator，在基线树上连续更新 16 轮后，
  Page file 的 `final_pages / baseline_pages > 1.25` 才进入 Compaction。
- 命令：`go test -run TestCompactionEvidenceGate -v ./internal/store/currentrowindex`
- 环境：darwin/arm64，Go 当前项目 toolchain，16 KiB Page，单实例 Buffer Pool。
- 结果：基线 5 Page，最终 5 Page，空间放大 `1.00x`；未越过 `1.25x`。
- 结论：当前等宽 revision churn 走原位 Page 更新；不为尚未出现的空间问题加入
  generation rewrite、reader handoff 和回收故障域。

## 后续门

F152–F163 到达时在本文件追加冻结条件、命令、环境、原始摘要和结论；如果条件成立，
先另开实现 Feature，不把大实现塞进证据门提交。
