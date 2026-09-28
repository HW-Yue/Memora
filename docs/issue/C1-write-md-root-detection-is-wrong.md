# C1 · Skill 的 `write.md` 教错「这张表有没有根」

严重度：Skill 文本 bug（照做会重复建根）。状态：**已关**（2026-09-26 复核，同日修复）。

## 结案

那一句改成：空数组只说明**还没挂东西**；`AT ROOT` 列的是根的子节点，所以没有根与有根但空着
返回一模一样；也没有任何查询能报出根本身（`SHOW ROUTES` 只认 `UNDER :id` 或 `FROM TABLE … AT ROOT`，
`DESCRIBE TABLE` 不带 route root id）。判据来自**建根那一步的拒绝**：
`table "notes" already has a route root`。

- 引擎行为本来就是对的，**没有 RED 可言**——缺陷在文本。可复现的观测来源是
  [audit-2026-09-23.md](./audit-2026-09-23.md) C1 的实测记录。
- 加了 `TestRootBootstrapIsLearnedFromTheRefusal`（`internal/sqlstore`）钉住这条指令**依赖**的两件事：
  无根与空根给出同样的空页；重复建根的拒绝码（`already_exists`）与措辞。指令教人读拒绝，
  拒绝本身就必须有测试。
- 证据：`skill(write)` 分支 → merge `af1ba112`；Skill 六处同步后 `--check` = every copy matches。

## 症状

`references/write.md` 的 "Bootstrap a Router on a Table that has none" 第 1 步说：

> Confirm the Table really has no root yet — **an empty rows array means none**.

**错。** `SHOW ROUTES … AT ROOT` 列的是根**下面的子节点**，不返回根自己 —— 根刚建好、还没挂东西时
同样返回空数组。实测：`CREATE ROUTE ROOT` 第一次就成功了，照这句话复查看到 0 行，以为没成，再跑一次
才报 `table "models" already has a route root`。

后果：照 Skill 做会重复建根；更坏的是有人据此判定"没有根"，转去走别的分支。

## 证据

- `skills/memora/references/write.md:283`（2026-09-26 合入 `ALTER DATABASE … SET` 之后在 main 上复核，文本仍未改）。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) C1。

## 已定的修法

把那句话改成"空数组只说明没有子节点；判断有没有根看建根那一步的返回，重复建根会报
`already has a route root`"，**或者**给一条能真正读出根的命令。

## 坑

改 Skill 要走六处同步：`scripts/sync-skill.sh --repo` → 每个 `--install` → `--publish` + 推送。
本次沙箱不能写仓库外的安装位，所以这条要单独开一次会话/环境做完收尾动作，别只改仓库里的副本。
