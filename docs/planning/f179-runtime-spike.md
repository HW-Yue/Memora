# F179：Agent Runtime Spike 与 ADR

规划状态：已完成并通过完成门。

## 唯一主要结果

用同一份最小查询 loop 需求对照 Eino 与 Memora-owned 薄状态机，形成是否引入运行时框架的
ADR。F179 不接真实 Provider、不实现 Query Agent、不修改 daemon/CLI，也不把任一 spike
依赖加入生产 `go.mod`。

## 固定比较面

- 两个候选都执行 `bootstrap → provider → tool → provider → final`，工具只接抽象端口；
- 验证 `context.Context` 取消、严格最大步数、tool-call 分派与 checkpoint/resume 表达能力；
- Eino 使用执行日最新稳定版，仅引入完成比较所需的最小 package；
- 用相同 Go、GOOS/GOARCH、`CGO_ENABLED=0`、`-trimpath -ldflags=-s=-w` 构建；
- 记录二进制字节数、链接 package/module 数、固定工作负载峰值 RSS，并明确机器与测量命令；
- 核对上游许可证、维护边界及 Memora Provider/MSQL/Trace 类型是否需要适配或泄漏。

体积/RSS 只作为当前机器上的方向性证据，不伪装成跨平台性能承诺。取消、checkpoint、tool-call
以可运行 spike 和上游源码/文档交叉验证，不只依赖 README 宣称。

## 决策门

只有当框架替代了 Memora 当前确实需要维护的复杂能力，且不破坏 MSQL-only、Provider-neutral、
Trace-owned 与单一可执行文件边界时才进入生产依赖。否则 F180/F181 使用薄 loop，并把 Eino 保留为
未来多 Agent、复杂人工中断或编排需求显著增长后的可替换候选。

## 完成证据

- 两个隔离 spike 的行为测试与构建通过；
- 版本、校验和、构建参数、size/RSS/dependency 结果可追溯；
- ADR 明确选择、理由、代价、重新评估触发器和 F180/F181 约束；
- 主模块 `go.mod/go.sum` 无生产依赖变化，完整 CI 与 cross-build 全绿。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F179 范围。

开工前结论：PASS。

## 完成结果

- 两个隔离 module 的 cancel、tool dispatch、checkpoint/resume 测试已从 RED 转为 GREEN；
- Apple M4/16 GiB、macOS 15.7.3、Go 1.26.5 下完成七次 RSS 与 stripped binary/package/module 对照；
- Eino v0.9.13 功能与 Apache-2.0 许可满足要求，但叠加当前 Memora 入口使二进制增加 8,368,544 B；
- [ADR-0009](../decisions/0009-memora-owned-agent-loop.md) 已选择 F180/F181 使用 Memora-owned 薄 loop；
- Eino 仅存在于可复现 spike 的嵌套 module，主模块 `go.mod/go.sum` 未变化。

完成门结论：PASS。
