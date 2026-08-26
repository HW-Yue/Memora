# 待发布的对外可见变化

发布说明由 CI 在发版时生成（见
[GitHub Release 自动化](./github-release-automation-v1.md)），
本文是它的**素材来源**：凡是**对外可见的行为改变或能力缩减**，
落地那一刻就记在这里，发版时逐条搬进 release notes 并清空本文。

只记「用户或客户端会察觉到不一样」的事。内部重构、性能改进、
纯新增能力不进这里。

## 未发布

### Route 节点的 revision 会被数据写入推高

**契约变化。** 叶子现在记下它持有的 Row，那是对节点记录的一次修改，
必然带来新 revision。于是 `ALTER ROUTE` / `DELETE ROUTE` 的
`EXPECTED REVISION` 可能被一次**数据写入**打断——你按创建时的 revision
读了叶子，有行挂了上去，你的编辑就撞版本冲突。

**客户端要做的事**：编辑一个叶子前重新读它的 revision，不要沿用创建时那个。

背景与取舍见[叶子直挂 RowID](../storage/leaf-rowid-v1.md) §6.1。

### 语义健康报告少三项

**能力缩减。** `memora maintain --report` 不再产出这三类问题：

| 问题项 | 为什么消失 |
|---|---|
| `stale_membership` | 叶子上不存 Row 的 revision，无从过期 |
| `invalid_membership_scope` | 叶子自己带 Database/Table，不存在第二份可以对不上 |
| `multi_row_leaf` | 一个字段放不下两个 RowID |

**不是修好了，是不可能发生了**——这三类问题描述的都是一个独立
Membership 对象与它两端漂移出来的状态，那个对象已经不存在。

保留的 `unrouted_row`（活行没挂在任何叶子下）与 `orphan_membership`
（叶子指向一个不存在的 Row）改为读叶子字段判定，行为不变。

消费健康报告的客户端如果按 `kind` 硬编码分支，需要删掉这三个分支；
按 `kind` 白名单过滤的不受影响。

### 文档解析的字节上界下调

**能力缩减。** 三个解析器的默认字节上界与实测能力对齐：

| 配置 | 原值 | 新值 |
|---|---|---|
| `DefaultEPUBAdapterConfig.MaxTotalUncompressedBytes` | 512 MiB | 64 MiB |
| `DefaultDOCXAdapterConfig.MaxTotalUncompressedBytes` | 512 MiB | 64 MiB |
| `DefaultPDFAdapterConfig.MaxFileBytes` | 128 MiB | 32 MiB |
| `DefaultPDFAdapterConfig.MaxDecompressedBytes` | 512 MiB | 64 MiB |
| `DefaultPDFAdapterConfig.MaxStreamBytes` | 64 MiB | 32 MiB |

原上界**允许**能打爆进程的输入：按峰值堆 ≈ 正文 7 倍推算，
一个刚好卡在 512 MiB 上界的文档要 3.5 GB 堆。
一个拦不住它存在来拦的东西的上界，不是上界。

典型文档正文 1–5 MiB，不受影响。上传超过新上界的文档会被拒绝，
调用方可以自己传更大的 config 覆盖默认值。

## 关联

- [叶子直挂 RowID](../storage/leaf-rowid-v1.md)
- [语义健康 v2](../agent/semantic-health-v2.md)
- [GitHub Release 自动化](./github-release-automation-v1.md)
