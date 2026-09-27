# C1 · Skill 的 `write.md` 教错「这张表有没有根」

严重度：Skill 文本 bug（照做会重复建根）。状态：**待做**（已在主线上复核仍然错的）。

## 症状

`references/write.md` 的 "Bootstrap a Router on a Table that has none" 第 1 步说：

> Confirm the Table really has no root yet — **an empty rows array means none**.

**错。** `SHOW ROUTES … AT ROOT` 列的是根**下面的子节点**，不返回根自己 —— 根刚建好、还没挂东西时
同样返回空数组。实测：`CREATE ROUTE ROOT` 第一次就成功了，照这句话复查看到 0 行，以为没成，再跑一次
才报 `table "models" already has a route root`。

后果：照 Skill 做会重复建根；更坏的是有人据此判定"没有根"，转去走别的分支。

## 证据

- `skills/memora/references/write.md:230`（2026-09-26 在主线 `main` 上复核，文本未改）。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) C1。

## 已定的修法

把那句话改成"空数组只说明没有子节点；判断有没有根看建根那一步的返回，重复建根会报
`already has a route root`"，**或者**给一条能真正读出根的命令。

## 坑

改 Skill 要走六处同步：`scripts/sync-skill.sh --repo` → 每个 `--install` → `--publish` + 推送。
本次沙箱不能写仓库外的安装位，所以这条要单独开一次会话/环境做完收尾动作，别只改仓库里的副本。
