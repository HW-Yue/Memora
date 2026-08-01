# F124 Route Benchmark Corpus 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-COLD、US-READ、US-DEVELOPER；
- 用户结果：后续五个 retrieval arm 在同一 Router 题库和 ground truth 上公平比较；
- 唯一主要结果：predictor 实现前冻结可重复的 Route tree/question/path corpus；
- 明确不做：真实模型调用、Lexical/Vector、embedding、Runner、指标结论；
- 开工前结论：PASS。

## RED 清单

- 没有版本化 Route corpus，旧 AI benchmark 也不含 fanout/depth path ground truth；
- 题目、aliases、树和 expected path 可在实现 predictor 后任意修改；
- fanout/depth/difficulty/language 覆盖不完整；
- 顺序依赖运行时随机数，重复运行不同；
- path 断裂、RowID 不属于 leaf、负例仍读 Row 或 stable ID 泄漏到问题；
- JSON 接受 unknown field，snapshot/corpus hash 篡改不被发现；
- 中间 target 是唯一 branch，模型可绕过语义直接猜结构。

RED 命令：

```text
go test ./internal/routebenchmark
```

RED 已因 routebenchmark package、冻结 generator 和 corpus artifact 均不存在而失败。

## 完成门

- strict loader、deterministic generator、scenario snapshot 与 corpus hash；
- 30 个 scenario 完整覆盖 6×5 fanout/depth，并覆盖五难度、三语言、六主题；
- tree/path/RowID/negative/budget/seeded shuffle 不变量和 tamper rejection；
- checked-in artifact 与 generator deep-equal；
- `scripts/ci.sh` 全绿并同步设计与规划；证据满足前保持 `INCOMPLETE`。

## 完成证据

- RED 先证明 routebenchmark package、deterministic generator 和冻结 artifact 均不存在；
- `memora.route-benchmark-corpus/v1` 固定 30 个 scenario，完整覆盖 fanout
  4/8/12/16/24/32 × depth 1/2/3/4/6，并覆盖五难度、三语言和六主题；
- 每题携带完整 stable-ID Router、distractor RowID、seeded order、snapshot digest、
  ground-truth path/negative stop 与 corpus hash；target/decoy 在中间层均为 branch；
- strict loader 拒绝 unknown field、重复/断裂树、RowID 重复、path 错配、snapshot 与
  corpus tamper；checked-in 1.5 MB JSON 与 generator 输出逐字一致；
- 每个 scenario 确定转换为 F123 Real Host Task，绑定同一 corpus digest、Database scope
  与 tool/context/time 预算；30 个 Task digest 均唯一；
- race、全仓 CI、integration/e2e 与 cross-build 通过，完成结论为 `PASS`。
