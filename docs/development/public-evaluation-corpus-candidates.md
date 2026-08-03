# 公开评测语料候选

状态：候选；不改变 F182 自有合成语料，也不构成下载、再分发或实现授权。

## 当前结论

F182 使用项目自有的小型合成 fixture，目的是确定性验证 Database/Table/Route/RowID、权限、
多事实和无答案链路。它不依赖模型预训练记忆、在线文档变化或第三方 chunk 格式。

公开数据集只作为后续外部效度对照。每套 adapter 必须独立冻结版本、数据子集、许可、转换规则、
MSQL 物化摘要和评分含义；不能把检索 qrel、最终答案正确率和 Memora 内部 Route 诊断混成一个分数。

## 候选分工

| 数据集 | 适用证据 | 当前边界 |
| --- | --- | --- |
| [CRUD-RAG](https://github.com/IAAR-Shanghai/CRUD_RAG) | 中文 RAG 问答、摘要、续写与噪声场景 | 优先候选；引入前逐文件确认数据许可，不直接打包 8 万新闻文档 |
| [RGB](https://github.com/chen700564/RGB) | 中英双语噪声鲁棒、无答案拒绝、多文档整合、反事实鲁棒 | 适合验证“相似内容不能冒充事实”；先冻结可再分发子集与 evaluator |
| [HotpotQA](https://hotpotqa.github.io/) | 多跳答案与 supporting facts | CC BY-SA 4.0；适合映射多 Row evidence，不替代中文主集 |
| [MIRACL](https://github.com/project-miracl/miracl) | 中文 query/qrel、BM25/向量/混合检索对照 | 只评价 retrieval，不提供统一最终答案，不能单独证明 Agent 回答正确 |

## 进入条件

1. F183 能从干净 Instance 只经 MSQL 物化 F182，并稳定输出 answer/evidence/trace；
2. F184 已区分最终答案分数、retrieval 指标和 private diagnostics；
3. 数据许可允许预期的下载、转换、缓存、报告和再分发方式；
4. 转换后的每个事实有确定性 Row/Column/source 坐标，ground truth 不暴露给 Query Agent；
5. 公共集结果单独报告，不覆盖 F182 regression gate。
