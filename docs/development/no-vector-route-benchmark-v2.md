# 无向量语义 Route Benchmark v2

状态：F124–F126 讨论稿，待用户 Review；未获实现授权。

## 核心问题

1. 一层 Route 展示多少个兄弟节点时，模型仍能稳定选中正确分支？
2. 树变深后，逐层小误差怎样累积为最终 RowID 失败？
3. Codex、Claude、Kimi 与其他宿主模型的安全 fanout 有多大差异？
4. 节点语义相近、查询改写、顺序变化和预算收紧时，结果是否仍可靠？

评测只让模型阅读节点描述并逐层选择稳定 ID。评分器按预先标注的 Route ID、
RowID 和路径精确比较，禁止 Embedding、Vector、cosine、隐藏相似度或全文扫描。

## 候选实验矩阵

首轮候选值，最终在 F124–F126 Review 时冻结：

| 变量 | 候选值 |
| --- | --- |
| 每层 fanout | 4、8、12、16、24、32 |
| Route depth | 1、2、3、4、6 |
| 语义难度 | 明显分离、相关主题、边界重叠、同义改写、负例/不应进入 |
| 叶子 locator 数 | 1、4、8、16、24 |
| 候选顺序 | 产品确定性顺序、seeded shuffle |
| 查询语言 | 中文、英文、混合术语 |

受控用例只构造“正确路径 + 每层兄弟干扰项”，不需要生成 `32^6` 个节点；另用少量
真实不规则树验证受控结论能否迁移到实际 Database。

每个 cell 必须有足够独立问题和重复运行，报告样本数、随机种子与置信区间；不能
只挑成功示例，也不能在看到结果后修改门槛。

## 固定输入与公平对照

模型之间共享：

- 相同 Database/Table/Route snapshot 和 ground truth；
- 相同 Canonical Skill、MSQL、Route Frame 字段和字符预算；
- 相同查询集合、候选顺序 arm、最大回退次数和停止条件；
- 相同冷启动要求，不读取旧聊天或先前答案。

每次 run 记录 host、Provider 协议、model identity/version（如果宿主可提供）、
Skill digest、suite digest、参数、时间和 endpoint label；不保存 API Key。Provider
和 Key 继续由 Codex、Claude、CC Switch 等宿主管理，Memora 不直接调用模型。

宿主和模型无法完全解耦时，报告写成 `host + model` 组合，不把宿主差异伪装成纯
模型差异。支持 seed/temperature 的接口记录配置，不支持时如实标记。

## 分层指标

- `level_top1_accuracy`：每一层第一次是否选择正确 child ID；
- `level_target_recall`：允许选择少量分支时，目标是否在有界选择中；
- `exact_path_success`：整条 Route 路径是否无误；
- `rowid_success`：最终是否得到 ground-truth RowID；
- `wrong_branch_rate`、`abstain_rate`、`backtrack_recovery_rate`；
- 每层和全程的调用数、输入/输出字符、token、延迟与费用；
- Route Frame 峰值、truncation、无关 locator 和错误正文读取数。

结果必须同时按 fanout、depth、难度、语言、host/model 分桶，不能只汇总成一个
Recall。每层准确率和端到端成功率都必须报告，否则看不出误差在哪一层累积。

## 安全 fanout

每个 `host + model + difficulty` 输出能力曲线。`safe_fanout` 定义为：在预先冻结的
逐层准确率、最终 RowID 成功率、调用数和 Route Frame 预算下，置信区间下界仍通过
门槛的最大 fanout。

产品还要计算目标模型集合的 `shared_safe_fanout`。同一 Database 只有一棵权威
语义树，默认值取共同可靠范围，不为不同模型维护彼此漂移的 Route Tree。能力更弱
的模型需要更细的树；能力更强的模型可以减少调用，但不能绕过相同 MSQL 协议。

Benchmark 只提供证据，不自动修改配置。Router fanout 变更仍要经过可视化影响
预览、revision、Policy、局部 split/merge、观察期和回滚。

## 失败分析

每次失败保存可审计但不含隐藏推理的证据：

- 模型当层实际看到的节点 ID、描述和顺序；
- 模型选择的 ID、允许的简短公开理由、错误码和是否回退；
- ground-truth child/path/RowID；
- snapshot、预算、调用计数和最终 SQL 结果；
- 失败类型：描述含混、边界重叠、位置偏差、预算截断、过早停止或协议错误。

这些证据后续进入 Studio Route Trace/Benchmark 页面，支持按模型叠加能力曲线和
定位需要 split、rename 或改写 synopsis 的节点，但不自动修改事实或索引。

## 完成门

- 至少覆盖 Codex、Claude 和一个经自定义 OpenAI-compatible endpoint 接入的模型；
- 所有 run 都由真实宿主模型逐层选择，不接受 scripted Route ID；
- 公开原始分桶计数、失败样本、suite digest 和可复算报告；
- 能回答每个模型及共同目标集合的安全 fanout，而不是只给总体 Recall；
- 重跑能解释波动，未知模型身份或缺失样本必须标记 `INCOMPLETE`；
- 评测代码和依赖中不存在 Vector/cosine 路径。

## 关联

- [语义树检索质量链路](../query/retrieval-quality.md)
- [AI-native 质量模型](../product/quality-model.md)
- [宿主模型与 CC Switch 兼容边界](../agent/host-provider-compatibility.md)
