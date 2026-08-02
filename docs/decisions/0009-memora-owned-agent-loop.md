# ADR-0009：首个查询 Agent 使用 Memora-owned 薄 Loop

状态：Accepted，F179。

## 背景

F176–F178 已由 Memora 自己冻结 Bootstrap、Provider、MSQL-only 与 Trace contract。F181 的首条
垂直链是固定的只读 benchmark loop，不需要多 Agent、任意 DAG 或并行工具调度。F179 对照
Eino Compose 与显式状态机，判断是否值得为尚未使用的编排能力增加生产依赖。

## 可复现实测

环境为 Apple M4/16 GiB、macOS 15.7.3 arm64、Go 1.26.5。两个隔离 module 都执行 10,000 次
`bootstrap → provider → tool → checkpoint → provider → final`，验证取消与恢复；构建统一使用
`CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'`。RSS 是 warm filesystem 后七次
`/usr/bin/time -l` 的中位数。

Eino 采用执行日最新稳定版 `v0.9.13`，module sum 为
`h1:iD/ETS+lxnNp1VeNPqWVGPWdND6Dbf4LyINbLUlDRcM=`。

| 证据 | 薄状态机 | Eino Compose | Eino 增量 |
| --- | ---: | ---: | ---: |
| 最小 workload 二进制 | 2,078,722 B | 10,565,698 B | +8,486,976 B（5.08x 总量） |
| 最小 workload 峰值 RSS 中位数 | 9,240,576 B | 29,163,520 B | +19,922,944 B（3.16x 总量） |
| 链接总 package / 非标准 package | 67 / 1 | 224 / 88 | +157 / +87 |
| 链接依赖 module | 0 | 24 | +24 |
| 当前 Memora 等价入口二进制 | 10,272,162 B | 18,640,706 B | +8,368,544 B（+81.47%） |
| 入口执行一个 Graph 的峰值 RSS 中位数 | 8,978,432 B | 12,795,904 B | +3,817,472 B（+42.52%） |
| 入口链接总 / 非标准 package | 270 / 79 | 369 / 164 | +99 / +85 |
| 入口链接依赖 module | 2 | 25 | +23 |

结果是当前机器上的方向性工程证据，不是跨平台性能承诺。可运行样例、测试和命令保存在
`tools/runtime-spike/`；生成的二进制不提交，主模块 `go.mod/go.sum` 不含 Eino。

## 能力核对

Eino 原生支持带 `context.Context` 的 Graph、ToolsNode、interrupt/resume 与可注入
CheckPointStore；其 [v0.9.13 源码](https://github.com/cloudwego/eino/tree/v0.9.13)和
[Apache-2.0 许可证](https://github.com/cloudwego/eino/blob/v0.9.13/LICENSE-APACHE)没有许可阻碍。
但 Eino checkpoint 持久化的是框架 graph/channel/state，Memora 仍需另外定义版本化、正文有界、
可审计的 Query Workspace；Provider/MSQL/Trace 也仍需双向 adapter。因此当前引入它没有删除
Memora 的权威 contract，只增加第二套 runtime 类型和升级面。

## 决策

F180/F181 使用 Memora-owned 薄 loop，不把 Eino 或其他 Agent 框架加入生产依赖：

- loop 是显式、类型化、最大步数受限的状态机，只依赖现有 Provider、MSQLExecutor、Bootstrap 和 Trace；
- 每个模型等待前不持有 MSQL transaction，取消贯穿 Provider 与 MSQL 调用；
- tool-call 只严格解码为允许的完整 MSQL request，框架输出不能绕过 Parser/Policy；
- F181 的 benchmark loop 不做 durable checkpoint；F186 若产品化 QuerySession，再冻结 Memora-owned
  Query Workspace/恢复 contract；
- 保持 runtime 构造注入点，使未来替换编排实现不影响数据库内核或公开协议。

Eino 仍是合格候选，不是被判定为质量差。出现嵌套人工中断、多个独立可恢复分支、并行工具图或
多 Agent 调度的已批准需求，且自研调度复杂度开始超过有界状态机时，重新执行 size/RSS、类型泄漏、
checkpoint 兼容和许可证 Review；在此之前不为候选能力付常驻成本。

## 结果

F180 可直接实现 OpenAI-compatible HTTP Provider，F181 在现有 Memora-owned contract 上组装最小
查询闭环。Provider 厂商、Eino schema 或框架 checkpoint 类型不得进入 MSQL、数据库或 Trace 协议。

## 关联

- [F179 Runtime Spike](../planning/f179-runtime-spike.md)
- [可选内置 Agent Runtime](../agent/embedded-agent-runtime.md)
- [Agent MSQL 边界](../agent/agent-msql-dependency-injection.md)
