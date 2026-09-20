# Committed Change Page Index v1

状态：F113 已完成并验收；2026-08-01 冻结 Page locator 与 reconcile 边界。

## 唯一职责

独立 derived Page B+ Tree 将 change commit sequence 与 transaction ID 映射到 F109
immutable envelope identity。value 只保存 sequence、transaction ID 和 checksum；正文
仍从 native committed-change body 读取并重新校验。

## 物理边界

- 位于 Database 目录下独立 `change-index-v1/`，不修改 F106 三树 generation 格式；
- 使用自己的 Page file、WAL、Tree Control 和固定 Space ID；
- 首次以 staging + rename 构建，open/reopen 严格验证 Page/WAL；
- append 同时写 sequence key、transaction key 和 high-water key；三者一个 Tree commit；
- sequence 必须从当前 high-water 连续递增，重复 locator 仅允许完全幂等。

## Reconcile

open 和 MSQL read 在 Authority operation gate 内比较 Page high-water 与 immutable body
high-water，只追加缺失 suffix。Page 领先 body、已有 locator 与 body identity/checksum
不一致或序列出现洞都视为 corruption；产品查询不扫描 body 充当结果 fallback。

## Read view

一次 timeline 首屏固定 high-water。B+ Tree range 只读 `(after, high-water]`，再按已授权
Database scope 读取并校验 envelope。新 commit 可以进入 index，但旧 cursor 始终停在旧
high-water，因此 continuation 不混入新事务。
