# F214：外部语料到 Retrieval Suite 的确定性适配器

状态：已授权，开发中；2026-08-06 冻结。

## 单一结果

把已下载的公开数据源 query/qrel 文件转换为 F213 `memora.retrieval-suite/v1`，保留公开 query 文本、
稳定 query/document ID、split、group 和 relevance；适配器不调用模型、不读取答案正文、不写入 Memora，
也不把原始文档机械切片成 Memora Row。

首版支持两个入口：

- `miracl-zh`：MIRACL 中文 train/dev topics 与 qrels；
- `mtrag`：MTRAG Human 指定 domain 的 last-turn/rewrite 查询与 BEIR dev qrels。

EnterpriseRAG parquet、CRUD-RAG 自有任务、RGB 任务和大文档吸收仍需各自明确字段/许可边界，不能用
“JSON 解析成功”冒充已适配。

## 规范化规则

- 只读取公开 query 文本和 qrel，不把答案、正文或隐藏 evaluator 资料交给 Agent；
- 每个 suite 只保留有至少一个正 qrel 的 query，空答案题另走答案/拒答评测；
- MTRAG 保留上游 chunk/document ID 原样，后续若映射到 Memora Row 必须另存 source locator 映射，
  不悄悄把 chunk ID 当 RowID；
- 所有输入按 UTF-8、TSV/JSONL 严格解析，重复 query、重复 qrel、孤儿 qrel、缺列、非法 relevance
  fail closed；输出先 `Suite.Seal`，同目录临时文件发布且拒绝覆盖；
- 生成的 suite 写到外置评测根的 normalized 目录，不进入 Git。

## CLI

```text
go run ./cmd/normalize-retrieval-suite \
  --kind miracl-zh --root /Volumes/yhw/MemoraEvaluation/raw/miracl-zh-v1 \
  --split dev --output /Volumes/yhw/MemoraEvaluation/normalized/miracl-zh-dev-suite.json

go run ./cmd/normalize-retrieval-suite \
  --kind mtrag --root /Volumes/yhw/MemoraEvaluation/sources/mtrag-human-v1 \
  --domain clapnq --query-mode lastturn --output /Volumes/yhw/MemoraEvaluation/normalized/mtrag-clapnq-suite.json
```

输出 suite 的 `Query.Text` 是 Agent 的公开问题输入；F213 scorer 报告不会复制它。

## RED 与完成门

RED 使用本地 MIRACL 风格 fixture：TSV 两条 query、一个 qrel 缺失正例或一条孤儿 qrel 时必须稳定失败；
GREEN 后验证真实 MIRACL dev 393 queries/3,928 judgments，以及四个 MTRAG domain 的 query/qrel 数量、
suite hash 和离线重复运行摘要一致。执行 targeted、全量、race、vet、diff check。

完成证据：MIRACL zh dev 已生成 393 queries/3,928 qrels；MTRAG last-turn 四域分别为
ClapNQ 208/578、Cloud 188/494、FiQA 180/535、Govt 201/521，并额外生成对应 rewrite suites；
所有 suite 均通过 `Suite.Validate`，生成文件位于 `/Volumes/yhw/MemoraEvaluation/normalized/`，原始
数据仍由 F212 receipt 绑定。EnterpriseRAG parquet、CRUD-RAG 和 RGB 尚未宣称已适配。

## 关联

- [F212：外置评测数据准备](./f212-external-evaluation-data.md)
- [F213：外部检索评分与对照报告](./f213-retrieval-evaluation-score.md)
- [公开评测语料候选](../development/public-evaluation-corpus-candidates.md)
