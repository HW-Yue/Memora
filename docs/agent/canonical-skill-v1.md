# Canonical Skill v1

状态：稳定规则只维护在
[`skills/memora/SKILL.md`](../../skills/memora/SKILL.md)。

## 唯一来源

面向独立 Agent 安装的同步发布仓库是 [HW-Yue/memora-skill](https://github.com/HW-Yue/memora-skill)。
产品定位与读写流程按需维护在
[`skills/memora/references/product-manual.md`](../../skills/memora/references/product-manual.md)，
不包含任何动态 Database 状态。

契约绑定：

- `memora.skill/v1`；
- `memora.msql.ast/v1`；
- `memora.result/v1`；
- `memora.semantic-conflict/v1` 和 `memora.conflict-resolution/v1`；
- `memora.route-mutation-proposal/v1` 和 `memora.route-mutation-plan/v1`；
- `memora doctor/query/exec/mutate/schema`。

每次 CI 都解析契约中的 MSQL 示例。版本或语法变化必须显式更新契约和 golden。
首次安装例外只允许相邻 `scripts/install.sh`；必须先获得用户授权。

## 稳定流程

```text
discover → query → summarize
         → write → receipt
         → request_user（发生语义冲突或越过风险边界）
```

发现阶段逐层 `SHOW ROUTES`。关键词与向量召回是产品上的另外两条路，入口待实现。
Router 只返回定位，宿主必须 SELECT 回表后才能回答。写入先查已有 Row，再选择
IGNORE、INSERT、REVISE、MERGE、SPLIT 或 MOVE。

语义冲突只展示双方来源、revision 和差异，必须等用户决定后才生成 mutation。

## 安全与上下文预算

Skill 禁止读取或修改物理数据库文件。数据库真相只来自版本化 MSQL Result。
v1 宿主侧硬上限为：

- Router 12 行；
- SELECT 10 行；
- Mutation Receipt 2,000 字符；
- 单任务工作上下文 12,000 字符。

这些值是版本化宿主协议的保守上限，不代替数据库内可演化的质量配置。

## 关联

- [AI-native 产品契约](../product/ai-native-contract.md)
- [Skill 写入](./skill-write-v1.md)
