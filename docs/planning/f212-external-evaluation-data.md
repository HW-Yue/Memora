# F212：外置评测数据准备

状态：已完成；2026-08-06 冻结并验收。2026-08-11 按
[ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md)转为 **Deferred**：
设施冻结保留、不删除、不再继续投入，恢复条件见该 ADR。

## 单一结果

Memora 能依据仓库内的冻结清单，把公开评测源安全、可续跑、可校验地准备到用户指定的绝对目录，
且原始语料、解压内容、归一化数据和索引均不进入 Git 或系统盘默认路径。

本机推荐通过环境变量指定：

```text
MEMORA_EVAL_ROOT=/Volumes/yhw/MemoraEvaluation
```

该变量只保存路径。Provider API Key 仍只从运行进程环境读取，不写入外置盘、清单、收据或报告。

## 冻结数据角色

| 数据集 | 冻结用途 | 许可边界 |
| --- | --- | --- |
| MTRAG Human | 842 个多轮任务、四领域检索与回答 | Apache-2.0 |
| CRUD-RAG | 超过 8 万篇中文新闻的规模与噪声检索 | 上游 README 声明 Apache-2.0 |
| RGB | 中文噪声、拒答、整合与反事实鲁棒性 | CC BY-NC-SA 4.0，仅非商业测评 |
| MIRACL zh | 393 个 dev query、3,928 条 qrel 与 4,934,368 个中文 passage | Apache-2.0 |
| EnterpriseRAG-Bench | 约 50 万企业文档、500 个问题的压力与外部效度 | MIT；保留上游 canary |

这些数据不直接成为 Memora 的机械 chunk 真相源。后续 adapter 必须把 source 坐标、文档、问题和
qrel 转为 evaluator 侧输入；写入 Memora 的仍是 Agent 吸收后的完整可修改语义模块。

## 清单与目录协议

仓库提交严格 JSON 清单，至少冻结：数据集 ID、用途、许可、上游 URL、Git commit 或 HTTP artifact
SHA-256、字节数和目标相对路径。未知字段、重复 ID、路径穿越、非 HTTPS URL、非完整 commit 或
缺少校验信息均 fail closed。

```text
<root>/
  sources/<dataset-id>/       # 固定 commit 的 Git 数据源
  raw/<dataset-id>/...        # 固定 SHA-256 的 HTTP artifact
  state/receipt-v1.json       # 不含正文和密钥的准备收据
```

- `--root` 必须是规范化绝对路径，不能是 `/`、用户 Home、仓库根或符号链接；
- Git 源在同目录 `.partial` 中拉取固定 commit，校验 HEAD 后 rename 发布；
- HTTP 源写入 `.part`，支持 Range 续传；服务端不接受 Range 时从零安全重启；
- 已发布目标只做校验，不静默覆盖；损坏、版本漂移或意外文件由用户显式处理；
- 收据通过同目录临时文件、flush 和 rename 更新，不依赖 ExFAT 的 Unix mode 语义；
- macOS 在 ExFAT 上可能旁写 `._*` AppleDouble 元数据；它不属于语料，准备器和后续 adapter 必须忽略；
- `--verify-only` 不访问网络，只复核完整 commit、字节数与 SHA-256。

## TDD 与验收

RED 入口：

```text
go test ./internal/evaldata ./cmd/prepare-evaluation-data
```

首个 RED 使用本地 HTTP server：第一次连接中断后保留 `.part`，第二次必须发送正确 Range、完成
SHA-256 校验并发布。它证明缺少的是可续传校验链路，不依赖公网、时间或坏 fixture。

边界矩阵：严格清单、重复 ID/路径、路径穿越、危险 root、symlink root、服务端忽略 Range、错误
长度、错误摘要、取消、已存在损坏目标、Git revision 漂移、重复执行与 verify-only 离线复核。

完成门除全量 Go 门禁外，还必须在外置盘实际准备冻结数据，生成收据并通过一次离线复核。后续
Recall@5/MRR、adapter、真实模型续跑和流式 Hook 聚合是独立 Feature，不混入 F212。

## 完成证据

- `go test ./internal/evaldata ./cmd/prepare-evaluation-data -count=1`：PASS；
- `go test ./...`、`go test -race ./...`、`go vet ./...`、`git diff --check`：PASS；
- 本地中断/Range 续传、服务端忽略 Range、摘要/长度错误、危险 root、路径穿越、Git revision 漂移、
  已发布损坏目标与 verify-only 均有确定性测试；
- `/Volumes/yhw/MemoraEvaluation` 已准备 5 个冻结数据集，约 3.1 GB；
- MIRACL zh 14 个 artifact 共 733,738,704 bytes，EnterpriseRAG-Bench 2 个 parquet 共
  1,410,301,868 bytes，MTRAG/CRUD-RAG/RGB 的 HEAD 均与清单 commit 一致；
- 全量 `--verify-only` 离线复核通过，无 `.part` 或 `.partial` 遗留。

## 关联

- [公开评测语料候选](../development/public-evaluation-corpus-candidates.md)
- [评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
- [F204 之后的开发计划](./post-f204-development-plan.md)
- [Feature TDD 协议](./feature-tdd-protocol.md)
