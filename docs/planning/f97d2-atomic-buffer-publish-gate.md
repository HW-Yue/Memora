# F97d2 Atomic Buffer Publish 开工与完成门

状态：已完成，PASS；授权来自 2026-07-31 后续 Feature 持续执行指令。

## 唯一结果

一个 committed 多 Page write set 可在硬容量 Buffer Pool 内全有或全无发布，新 Page
可安装，control 最后进入 committed view。

规格：[Atomic Buffer Publish v1](../storage/atomic-buffer-publish-v1.md)。

## RED Matrix

- 当前 Pool 只能逐 Handle Modify，不能安装新 Page 或原子发布 batch；
- existing + new + free + control 成功，值深复制、dirty FIFO control-last；
- durability 不足/查询失败、expected LSN 冲突、missing/loading/new-exists；
- duplicate、identity/type/generation/LSN、control 缺失/乱序/重复；
- 容量足够、clean victim 预选、只有 pinned/dirty victim 时原子 `ErrPoolFull`；
- Publish 与 Fetch/Inspect/Modify/Flush 的可控调度及 `-race`；
- flush 后 writer 收到全部 committed Page，WAL-before-data 保持。

## 完成门

- targeted `-count=20`、固定调度与 buffer package race；
- 全仓 test/race/vet、format、`git diff --check` 与 `./scripts/ci.sh`；
- 完成证据和独立原子 commit 同步后才进入 F97d3。

## 完成证据

- existing/new/free/control batch 深复制发布，dirty FIFO 保持 control-last，flush writer
  收到同一顺序；
- invalid identity/type/generation/LSN/control、durability fault、不足与 expected LSN/
  resident/loading/type 冲突均在零 Frame/LRU/dirty/eviction 变化下拒绝；
- clean victim 只在完整 preflight 后淘汰；pinned victim 返回原子 `ErrPoolFull`；
- Publish 与 Fetch/Inspect/Modify/Flush 的固定屏障调度以及 package race 通过；
- targeted `-count=20`、全仓 test/race/vet、format、`git diff --check` 与
  `./scripts/ci.sh` 全部 PASS。
