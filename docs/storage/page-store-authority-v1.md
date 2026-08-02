# Page Store Authority v1

状态：F107 已完成；2026-08-03 随 F172a 补充 generation v2 边界。

## 结果与边界

- generation 中的 Catalog、Current Row、Row Version 三棵持久树是新实例和已迁移实例的
  查询 authority；F172a 增加的 Fulltext Tree 是可重建派生索引，不成为正文 authority。
- `database.memora` 继续保存不可变对象正文，并且只在写入和启动恢复时作为 source；
  正常 Catalog/Row 查询不得枚举它的 Record 清单，也不得把它作为索引 miss fallback。
- F106 `manifest.json` 是只读迁移基线。激活后树允许继续演进，不能再用基线
  `content_sha256` 或初始 root state 判断 live generation。
- F108 才负责构建替代 generation、验证和原子 root/generation 切换；F107 不做 rebuild。

## 激活与打开

数据库目录使用 `page-authority-v1.json` 作为 durable authority marker。marker 绑定
F106 generation 目录、Plan digest 和 source fingerprint，并带自身 SHA-256 digest。

1. generation 不存在时，从当前已提交 Record 构建 F106 Plan 并原子发布 generation；
2. marker 不存在时，以 F106 严格模式验证内容、root state 和 source binding；
3. 原子写 marker、fsync 文件、rename，并 fsync 数据库目录；
4. marker 存在后只按 live 模式打开树：验证 marker/manifest 绑定、目录类型、Tree space
   和 WAL/Page recovery，不再要求 live 字节等于迁移基线；
5. 对外监听前扫描一次已提交正文并幂等补齐 Catalog、Version、Current 三棵 authority Tree；
6. 若 marker 指向合法三树 v1，启动过程用 COW replacement 发布四树 v2 后才返回；不原地修改 v1。

F172a 保证 v2 激活 seed，F172b 保证在线 Row revision 同步替换 Fulltext；Fulltext 仍是派生索引，
不改变三棵 authority Tree 与正文的权威关系。

marker 缺失时不能把已变化的 generation 猜成 authority；marker 损坏、绑定不一致或
live Tree 损坏都 fail closed。

## 发布协议

Authority 持有进程内 RW publication barrier。所有 authority read 持读锁；一个逻辑写
在写锁内完成：

```text
Catalog: commit immutable schema bodies -> replace Catalog tree -> publish success
Row:     preflight deterministic projection -> commit immutable Row/history bodies
         -> append Version tree -> replace Fulltext posting -> advance Current tree -> publish success
```

Version、Fulltext 必须先于 Current，且 reader 在同一 barrier 内看不到中间状态。每棵树使用其
WAL durable frontier 的下一个 transaction ID。任何正文 commit 后的 Tree 发布错误都
返回 outcome-unknown、毒化当前 Authority，并拒绝后续读写；重新打开时通过步骤 5
收敛。相同 Fulltext revision 是零 WAL replay；发现 revision gap 时用 COW generation replacement
重建，不能跳号掩盖漏删 posting。正文 commit 前的投影/commit 失败不改变 authority。

写入规划另经过一个可取消的 single-writer operation gate，防止 Schema 校验、正文
staging 与 Page publication 之间被另一逻辑写穿插；reader 在规划阶段不被阻塞。同一
Row 在进入该 gate 前先取得 F104 精确对象 try-lock，所以冲突可 fail-fast。这个保守
串行化只影响本地毫秒级写路径；只有基准证明它成为瓶颈时才允许细化 gate。

Catalog 的一批新 schema revision 以一个 native transaction 原子提交。native
transaction identity 由有序 Record 内容确定，保持字节确定性；随机事务 ID 不进入格式。

## 查询约束

- Database/Table/Column 通过 Catalog tree locator 读取精确正文；`SHOW` 通过有界前缀
  cursor 枚举 locator，不调用原生 `IDs()`。
- Get/List/As-of 通过 Current/Version tree locator 读取精确 Row revision。
- 索引 miss 直接返回 not-found；locator/body 不一致直接返回 corruption。
- snapshot high-water 只在 publication barrier 内捕获，不能观察 Version 已前进而
  Current 尚未前进的状态。

## 故障与并发证据

- 新空实例自动构建、激活，完成 DDL/Insert/Select 后可重开；
- 已有 F106 generation 可激活，stale source、损坏 marker/manifest/Page/WAL fail closed；
- 在正文 commit、Version publish、Current publish 和 marker rename/fsync 注入故障；
- outcome-unknown 当前进程不再服务，reopen 修复后结果与正文 reference model 一致；
- 毒化 Record 枚举 collaborator，证明正常 MSQL point/list/catalog read 不可到达旧扫描；
- 并发 reader/writer 在 `-race` 下只观察发布前或发布后快照。
