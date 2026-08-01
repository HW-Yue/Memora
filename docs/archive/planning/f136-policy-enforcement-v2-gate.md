# F136 Policy Enforcement v2 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

宿主提交的每条 MSQL 即使拥有正确 Database selector，也只能执行该库授权上限允许的
L0–L3 操作；等级判断和每库覆盖均由引擎确定性执行，Skill 只负责请求授权。

## 冻结语义

- L0：发现、导航、读取和只读计划。
- L1：同库、可补偿的 Row/Relation 写入。
- L2：Schema、Route、配置、安装、reshape 和跨库关系等结构性操作。
- L3：不可逆删除、历史清理、隐私降级和强制覆盖；v0 尚无公开 MSQL 入口。
- `default_level` 省略时按 L1 解释，以兼容现有 Go 调用；规范化宿主输入必须显式发送。
- `database_levels` 只能收紧或单独提升已列入 scope 的 Database，名称比较不区分大小写。
- 无 Authorization 的内部 Go/本地操作员路径保持既有信任边界。

## RED 清单

- Database selector 命中但等级不足时返回稳定 `permission_denied`。
- 同一 Authorization 对不同 Database 应用独立上限。
- 非法等级、越界 override、重复 canonical key 被拒绝。
- 同库 RELATE 是 L1，跨库 RELATE 是 L2；UNRELATE 按实际端点复核。
- Schema/Route/config/package 等结构操作不能靠 L1 scope 绕过。
- L2 的 hash-bound approval 仍需同时满足等级和既有精确 approval。

## 完成证据

定向 policy/executor/daemon/Skill tests 与 race 已通过；Canonical Skill 已重新生成且
`quick_validate` 通过；全仓 format、vet、unit、race、integration、e2e、cross-build 全绿。

下一项：F137 Package Signature。
