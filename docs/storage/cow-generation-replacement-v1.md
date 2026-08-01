# COW Generation Replacement v1

状态：F108 已完成并验收；2026-08-01 冻结。

## 唯一结果

已激活 Page authority 可在旧 generation 继续服务 reader 时旁路构建新 generation；
只有新 generation 完整验证并 durable 发布后才一次切换 authority。失败不能改变旧
Catalog/Current/Version root，也不能把 staging 暴露给 reader。

## Identity 与 marker

F107 初始目录仍为 `page-index-v1`，marker epoch 为 0。每次成功 replacement 使用：

```text
page-index-v1.g<20-digit-epoch>.<64-char-plan-digest>
```

marker 新增可选 `epoch`；epoch 0 的编码和 digest 与 F107 完全兼容，且只允许初始目录。
epoch N 只允许严格匹配上述目录，marker digest 覆盖 epoch、generation、Plan digest 和
source fingerprint。打开时不得扫描目录猜测最新 generation。

## Replace 协议

1. 取得 Authority single-writer operation gate；reader 继续使用旧 generation；
2. 从同一个 committed body source 构建 source-bound Plan；
3. 在数据库目录创建隐藏 staging，构建三棵全新 Tree；
4. 用 F106 strict open 校验 content digest、root state 和所有 locator/body reference；
5. reverify source，rename staging 到 epoch 目标并 fsync 数据库目录；
6. 以 live 模式打开新 generation，构造新的 Catalog/Row indexed reader；
7. 短暂取得 publication 写锁，原子写新 marker 并 fsync parent，然后交换内存指针；
8. 释放 reader 后关闭旧 generation。旧目录保留，回收属于后续 compaction/reuse 策略。

目标目录已存在时，只能在 strict 验证和 Plan binding 完全相同后复用；否则 conflict。
相同 source 也可以用更高 epoch 重建，generation identity 不等于内容 digest。

## 故障语义

- marker 发布前的 create/write/fsync/build/verify/reverify/rename/open 故障：清理 staging，
  marker 和内存 reader 不变，旧 generation 继续可读写；
- generation rename 后、marker 前故障：留下不可达 orphan，旧 authority 不变；retry
  可严格复用该目录；
- marker rename 后 parent fsync 故障：返回 outcome-unknown、毒化当前 Authority，
  reopen 只读取 marker 决定旧或新，禁止进程内猜测；
- marker durable 后旧 generation close 失败：authority 已确定为新 generation，报告
  资源关闭错误但不回滚 marker。

## 强制证据

- 旧 reader 在 build 阶段持续读到旧快照；成功后新 reader 读到等价完整数据；
- Catalog/Current/Version 每个 locator 与正文 reference model 对拍；
- build 三树、source reverify、generation rename/fsync、live open、marker rename/fsync
  全故障矩阵；
- retry/outcome-unknown/reopen 幂等；staging 永不成为 authority；
- 多次 replacement epoch 单调且 `-race` 无混合 reader。

## 完成证据

- epoch 0 marker golden 保持 F107 字节/digest 兼容，epoch generation identity、重绑定和
  路径穿越均严格拒绝；
- Catalog、32 个 Current Row、48 个 Row Version locator 与正文 reference model 逐项
  对拍；切换后写入和重开读取均通过；
- 三棵 Tree build、source reverify、generation rename/fsync、strict/live open、marker
  rename/fsync 及 marker durable 后崩溃均有 fault injection；
- 两个并发 replacement 串行得到 epoch 1/2，旁路构建期间 reader 持续读取，`-race`
  无混合 generation；
- `go test ./...`、`go vet ./...` 和 `scripts/ci.sh` 全绿。
