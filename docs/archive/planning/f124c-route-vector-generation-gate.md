# F124c Route Vector Generation 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

实现绑定当前 Route revision/surface 与单一 embedding space 的不可变向量 generation，
经旁路完整校验和 source reverify 后原子发布。

## 产品门

- 用户故事：可选本地/宿主/云 encoder 都能生成同一存储契约，缺失时正常 Router 回退。
- AI-native：只编码 AI 维护的 Route semantic surface，不复制事实或文档 chunk。
- 引擎边界：Agent 不管理 Page/manifest；Provider 密钥和模型运行时不进入 Memora。
- 可回滚：旧 active 在 marker 发布前始终可读，派生 generation 可删除重建。

结论：PASS。

## RED 与故障矩阵

入口：`go test ./internal/routevector`

1. 完整 publish/open/reopen：manifest、space、surface/revision、float32 bytes 确定且深复制。
2. 模型/dimension/digest/NaN/Inf/非归一化/重复或缺失 Route：发布前拒绝。
3. Route 在构建后变化：source reverify 失败；active generation 读取时也报告 stale。
4. staging write/sync、generation rename/sync、marker write/sync/rename/parent sync fault：
   marker 前旧 active 不变；marker 后 outcome-unknown 由 reopen 判定。
5. manifest、vector content、marker 篡改/未知字段/路径穿越：strict open 拒绝。
6. generation rename 后重试、marker outcome-unknown 重试、并发 publish：幂等或 revision conflict。
7. reclaim：只删除非 active 且不在 retain 集合的派生目录。
8. 类型边界：source/input 不接受 Row、History、membership 或原文 surface 持久化字段。

首次 RED 预期因 `internal/routevector` 不存在而失败。

## 明确不做

- 不生成 embedding，不下载或打包模型；
- 不实现 query、Top K、HNSW、Accelerate、GPU 或量化；
- 不修改 Database Package、Router authority 或 Row 存储；
- 不用旧 F25 三索引组合 manifest 承载新的 Route-only generation。

## 完成门

- failure matrix、targeted race、reopen/corruption/reference bytes 全绿；
- `./scripts/ci.sh` 全门通过；
- 旧 blanket CI 禁令收窄为禁止退役 Row 检索，并增加 Route predictor 不得依赖事实包门禁；
- 文档说明模型缺失回退、Instance 级分发和 F124d 的同 space 责任。

## 完成证据

- RED：首次运行 `go test ./internal/routevector` 因目标 package 尚不存在而失败。
- GREEN：`go test ./internal/routevector ./internal/nativerow ./internal/cicheck` 通过。
- Race：Route vector、原生 reopen 集成及全仓 race 门通过。
- 完整门：`./scripts/ci.sh` 通过 format、vet、unit、race、integration、e2e 与
  amd64/arm64 cross-build。
- 交付包含真实原生 Route source 的 publish → reopen → Route 变更 stale 证据；普通
  `SHOW ROUTES` 在 generation stale 后仍可用。
