# F191：内容寻址临时 SourceStore

状态：已完成，2026-08-05。

## 唯一主要结果

提供 Agent-owned 的临时原始资料存储：流式接收上传内容并校验 SHA-256，以内容摘要保存只读 Object，
再由 `(job_id, source_id)` 持久引用。Store reopen 后仍能安全读取；任务完成或取消时显式释放 Job
引用，并回收不再被任何 Job 使用的 Object。

SourceStore 不是 Memora Database，不进入 MSQL、snapshot、备份、Wiki 或倒排索引。F192/F193
只能从 SourceStore Reader 解析 Document IR，不能把原文复制到数据库。

## 物理与协议边界

```text
Put(job_id, source_id, expected_sha256, media_type, io.Reader)
→ staging stream + size/quota check + fsync
→ objects/<sha256>.source
→ refs/<job_hash>/<source_hash>.json

Open(job_id, source_id) → verified Reference + bounded-lifetime Reader
ReleaseJob(job_id) → remove refs → collect unreferenced objects
```

- content key 固定 `sha256:<64 hex>`；上传必须与 F189 inventory 的 expected digest 一致；
- 相同 `(job, source)`、相同规范 metadata/digest 为幂等 replay；不同内容为冲突；
- 相同内容可跨 Job 复用 Object，但每个 Job 的逻辑配额独立计数；
- 配额固定 max object bytes、max job bytes、max physical bytes 和 max sources/job；超过后不留 ref；
- 上传全程流式处理，不把整本书读进内存；Reader 失败、digest 不符或超配额时删除 staging；
- Object 与 ref 分别固定 `0600`，目录固定 `0700`，所有磁盘路径由 ID/digest 派生；
- Open/Resolve 校验 ref、regular-file、size 和完整 SHA-256；损坏或 symlink fail closed；
- 启动时清理遗留 staging，并回收没有 ref 的孤儿 Object；v1 只承诺单 Store 实例并发安全；
- ReleaseJob 是显式、幂等、可审计操作；F199 才把完成/取消策略接进长任务收据。

## RED 与完成门

- RED 先证明 SourceStore/config/request/reference/cleanup API 不存在；
- Put→关闭→reopen→Open 逐字读回，摘要、字节数、media type 和 metadata 不变；
- 同 ref replay、跨 Job dedupe、冲突拒绝、四类 quota 和并发 one-winner 均有测试；
- digest mismatch、Reader fault 和 object/ref symlink 不留下可见引用；
- staging/orphan crash residue 在 reopen 后清理；共享 Object 仅在最后一个 Job release 后删除；
- Object 篡改后 Open fail closed；根目录外文件不受任何清理操作影响；
- 目标测试、`-race`、Agent import allowlist 与完整 CI 全绿。

用户执行授权：2026-08-05 用户要求持续执行至 F204。

## 完成证据

- `Put` 使用 32 KiB buffer 流式写 staging，同时执行 context、object size 与 SHA-256 校验；
- Object/ref 使用摘要派生路径、`0600` 文件和 `0700` 目录，写入分别经 hard-link/atomic rename 与 fsync；
- reopen 后逐个验证 strict ref JSON、regular file、size 和完整 digest，并清理 staging 与 orphan；
- 相同 ref 幂等 replay，16 路并发只有一次 commit；相同内容跨 Job 只有一个物理 Object；
- object/job/physical/source-count 四类 quota、digest mismatch 和 Reader fault 均不产生可见 ref；
- Object 篡改、Object/ref symlink 均 fail closed；Release 只移除链接本身，根目录外 sentinel 保持不变；
- 共享对象在首个 Job release 后保留，最后一个引用释放后才删除，重复 Release 返回空收据；
- 目标测试与 `-race` 全绿，生产实现只使用标准库且没有接触 MSQL/Database 内部包。

完成门结论：PASS。下一项为 F192 格式无关 Document IR v1。
