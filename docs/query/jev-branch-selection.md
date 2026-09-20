# jev 作为逐层分支选择器

状态：**方向性结论**（2026-09-20）。查询侧的 Agent 侧选择器；四条路见
[检索路线](./retrieval-routes-jev.md)。

## 定位

jev 是 TypeSafe 的托管 System One 模型，走 HTTP API，返回带校准概率的类型化判断
（`Choice` / `Noul` / `Score`），不生成文本也不给推理解释。

它在 **Skill 侧（host 侧）** 调用，不进引擎，符合
[ADR-0007](../decisions/0007-route-predictor-arsenal.md)「引擎只接收版本化向量与模型身份，
不内置供应商调用」。逐层调用所需的基建已具备，见
[检索路线](./retrieval-routes-jev.md) 第 7 节。

用法：每一层把该页 child 的 name 与 purpose 作为 `Choice` 的 option 集，
取模型选出的那个 child 的 `route_id`，发下一条 `SHOW ROUTES UNDER`。
option 集**不含 route_id**——模型对随机 hex 无法推理，ID 由脚本按下标映射。

## 它不取消 fan-out 上限

候选集是**放进请求**发送的，并计入 token。因此「一层有多少 child」仍然直接换算成
请求体大小、成本与延迟。换用 jev **不消除结构 fan-out 约束，只是更换约束的来源**：
从「LLM 单层提示准确率」变为「一次 `Choice` 能可靠区分多少 option，以及愿为此付多少 token」。

写入路径本轮不动，判断者仍是原有 Agent，因此
[F223 的结构 fan-out 硬上限](../planning/f223-route-branch-fanout-limit.md)原样保留，
默认值 12 的原始理由完好。

## no-match 出口

`Choice` 从给定 option 集里必选一个。正确答案不在集合里时，它仍会挑一个最像的并给出
看似合理的概率。**「都不匹配」必须作为一个显式 option 存在**，否则这个信号永远收不到：
查询目标不在该子树时，导航不会报错，而会自信地走到一个无关叶子并回表。

不能用低置信度代替。`Choice` 的 confidence 是**分布集中度**，不是正确性：两个 child
都合适会摊薄概率，全都不合适也会摊薄概率——前者的正确动作是任选其一继续，后者是退回
上层，方向相反，阈值判不出来。

收到 no-match 时 Agent 的动作是回到父节点换分支、或改走关键词／向量召回，不是继续往下钻。

### 分页会让 no-match 产生歧义

「本页无合适项」不等于「本节点下无合适项」。当前 `branch_fanout` 与
`query_budgets.route_children` 都是 12，一层正好一页，两者等价。一旦放宽结构上限
使一层多页，no-match 必须区分这两种含义，否则会在第一页误判退出而正确答案在后页。

该缺陷**只在放宽 fan-out 之后出现**，因此当前测试不会发现它。

## 写入侧：暂不采用

写入侧的 no-match 等价于「这条内容不属于任何现有 child，该新建分支」，是 F223 计数
上限那个粗暴触发器的精确版本：上限用「数满了」迫使重构，no-match 用「语义上确实不属于」
判断。

本轮只改查询侧（读可回滚、写不可回滚），故暂不采用。若将来采用，它是唯一可替代
计数硬上限的触发器，届时需要重新评估 `branch_fanout` 的存在理由。

## 待决

- `Choice` 在多少 option 下仍可靠——需实测，不凭直觉；
- 查询侧是否引入 no-match 出口，以及是否同时解决分页歧义；
- 逐层 `Choice` 与一次跨多层 `Choice`（SKILL.md 的 fan-out / speculative 模式）的延迟对比。

## 关联

- [检索路线与内核面](./retrieval-routes-jev.md)
- [Route 读取协议](./route-read-v1.md)
- [ADR-0007：Router 权威，候选预测器可组合](../decisions/0007-route-predictor-arsenal.md)
- [F223：Route Branch Fan-out 硬上限](../planning/f223-route-branch-fanout-limit.md)
