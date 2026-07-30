# Canonical Skill v1

状态：F28 已冻结基础宿主契约；F30–F34 已扩展查询、写入、Schema、会话与语义冲突流程。

## 唯一来源

宿主稳定规则只维护在 [`skills/memora/SKILL.md`](../../skills/memora/SKILL.md)。
相邻 `contract.json` 是机器可读 lint 清单，不是第二份行为说明，也不保存
任何动态 Database、Schema、Router 或候选。

契约绑定：

- `memora.skill/v1`；
- `memora.msql.ast/v1`；
- `memora.result/v1`；
- `memora.semantic-conflict/v1` 和 `memora.conflict-resolution/v1`；
- `memora doctor/query/exec/mutate/schema/reflect` 六个逻辑入口。

每次 CI 都解析契约中的 MSQL 示例，并校验 Skill 中出现的是同一组命令。
版本或语法变化必须显式更新契约和 golden，不能让宿主提示静默漂移。

## 稳定流程

Canonical Skill 定义七个阶段：

```text
discover → query → summarize
         → write → receipt
         → assimilate → receipt
         → request_user（发生语义冲突或越过风险边界）
```

发现结果、Router/MATCH 候选和 SELECT Row 有不同语义。Router/MATCH 只返回
定位，宿主必须 SELECT 回表后才能回答或总结。写入先查已有 Row，再选择
IGNORE、INSERT、REVISE、MERGE、SPLIT、MOVE 或 RELATE。

资料只由宿主临时读取；覆盖、复核未完成时不得报告吸收成功。语义冲突只
展示双方来源、revision 和差异，必须等用户决定后才生成 mutation。
冲突 View 不持久化也不包含 SQL；用户决议通过新 event 绑定已展示
Row/revision，再转换为 IGNORE、REVISE 或 MERGE Plan。

## 安全与上下文预算

Skill 禁止读取或修改物理数据库、索引、日志、Page 和 Instance 文件，
数据库真相只来自版本化 MSQL Result。v1 宿主侧硬上限为：

- Router 12 行；
- 候选定位 24 行；
- SELECT 10 行；
- Mutation Receipt 2,000 字符；
- 单任务工作上下文 12,000 字符。

这些值是版本化宿主协议的保守上限，不代替数据库内可演化的质量配置。
后续修改需要新契约证据、测试和兼容说明。

## 关联

- [AI-native 产品契约](../product/ai-native-contract.md)
- [AI 自主权与约束](./autonomy.md)
- [上下文生命周期](../query/context-lifecycle.md)
- [Skill 语义冲突交互 v1](./skill-conflict-v1.md)
