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

## F152 Free Page Reuse

状态：门槛成立并已实现。

- 冻结门槛：320 个 Database 的 Catalog 基线删除末尾 10% 并加入等量新对象后，
  `final_pages / baseline_pages > 1.20` 即实现复用。
- 命令：`go test -run TestFreePageReuseEvidenceGate -v ./internal/store/catalogindex`
- RED：9→11 Page，浪费 22.22%，越过 20% 门。
- GREEN：分配优先使用同计划 recycled Page，再使用重启后从 durable free image 重建的
  free set；同一 workload 为 9→9 Page，浪费 0%。
- 恢复证据：retire/flush/reopen 后可发现 free Page；split 优先复用并保持 lookup 等价；
  非 free 候选、错误顺序/身份和 publish 冲突均拒绝。

## 后续门

F153–F163 到达时在本文件追加冻结条件、命令、环境、原始摘要和结论；如果条件成立，
先另开实现 Feature，不把大实现塞进证据门提交。
