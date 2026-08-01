# F97d Durable Tree Commit 拆分 Review

状态：拆分已完成；F97c4 与 F97d1–F97d3 均已独立验收，下一项为 F98。

## 发现的阻断冲突

Tree Control v1 和 Root Redo v1 把 `generation` 定义为每次 Tree commit 严格递增的
发布序号；F97a 则把 Page Header `generation` 定义为物理索引 generation，并要求一条
可达路径上的 Page generation 相同。

两者不能共用一个值。反例：

1. generation 3 的多层树只修改一个 leaf；
2. control 发布 generation 4，但未触及的 parent 仍为 generation 3；
3. 下一次 Planner 以 control generation 4 打开，合法 parent 被判 corruption。

这也与 ADR-0006 冲突：普通 Redo 更新不创建新 physical generation，COW generation
只用于 rebuild、compaction、snapshot 和整代 root swap。

## 修订决定

- `physical_generation`：Page Header 与 Tree Control Header 的 generation；普通提交保持
  不变，F108 整代替换时才改变；
- `revision`：每次 Tree root 发布严格加一，用于 redo 前置状态、幂等恢复和冲突检测；
- Tree Control v2 在 payload 中显式保存 revision；
- Root/Allocator Redo v2 使用 expected/next revision，不再把它称为 generation；
- F97c4 先完成格式、恢复与连续提交语义修正，F97d 不绕过该依赖开工。

## F97d 拆分

| Feature | 唯一主要结果 | 独立故障域 |
| --- | --- | --- |
| F97d1 Tree Commit Preparation | `(control, MutationPlan)` 严格校验并确定生成 Page/root/allocator redo | 纯转换、零 I/O |
| F97d2 Atomic Buffer Publish | 已有 committed LSN 的多 Page write set 在 Buffer Pool 原子发布，含新 Page 与 root-last | 容量、pin/latch、冲突、race |
| F97d3 Durable Tree Runtime | 单 writer 串联 WAL durable → buffer publish；失败 poison，reopen recovery 收敛 | WAL outcome、crash/reopen、服务状态 |

直接把三者放入一个 Feature 会同时引入编码协议、缓存并发协议和 crash 状态机，失败
无法由单一 RED 定位，不满足 Feature 大小门。

## 顺序

`F97c4 → F97d1 → F97d2 → F97d3 → F98`。用户的持续执行授权覆盖逐项开工，但每项
仍独立分支、RED、Review、完成门和合入。
