# 还要做什么

本目录只放**还没执行完的**计划与讨论稿。做完的规划搬到
[`../implementation/design/`](../implementation/design/)；做不完或要报的缺陷进
[`../issue/`](../issue/README.md)。

## 队列

- [执行计划](./execution-plan.md) —— **唯一队列**。逐项工单、顺序、判据都以它为准。
- [项目计划](./project-plan.md) —— 七个里程碑、判据与规模。
- [M7 召回详细计划](./m7-recall-plan.md) —— 七个 Feature、两次闭环、风险（待授权）。

## 未完成的设计与讨论稿

| 文件 | 还差什么 |
|---|---|
| [admin-canvas-connections.md](./admin-canvas-connections.md) | 第一刀已实现并真机验证；**第二刀（port 锚点）未做** |
| [engine-owned-shape.md](./engine-owned-shape.md) | **部分落地**：Skill 层约定已生效；列/描述的归属与描述字段的 amend 还在推进 |
| [jev-routing-ladder.md](./jev-routing-ladder.md) | **未开工**（等开工）；"jev 是默认执行器"一句已被决策日志反转，读时注意 |
| [row-navigable.md](./row-navigable.md) | 讨论稿；写入侧已实现，读取/拒绝口径待授权 |

## 常驻规则（不是任务）

- [feature-tdd-protocol.md](./feature-tdd-protocol.md) —— RED → GREEN → REFACTOR、Review 与合入协议。
- [feature-product-gate.md](./feature-product-gate.md) —— 每个 Feature 的产品与用户故事门禁。

## 一个例外

[whole-layer-read.md](./whole-layer-read.md) 是一份**指针**：正文已移到
[`../implementation/design/whole-layer-read.md`](../implementation/design/whole-layer-read.md)，
留在这里只是因为 Skill 直接引用了这个路径，而 Skill 的仓库外安装位本次不能同步（详见该文件）。
