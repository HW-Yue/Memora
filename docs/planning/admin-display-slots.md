# Admin 显示槽位改造

状态：讨论稿。方向已定（见[决策日志](../decisions.md) 2026-09-21 条），**未授权开工**。

## 规则

`ROLE summary` 的那份文档占视觉正中，且**整页只渲染一次**；其余一切（`purpose`、
`row_semantics`、系统列、其他业务列）都是元数据或属性，放周边并带标签。

## 现状（已从实例与资源核对）

- Row 页 `internal/adminui/dist/assets/rows.js`：`heading()` 第 265 行把 `row_semantics`
  放在标题下，266–268 行又把 summary 当**纯文本**加一段；`documentSection()` 288–294 行
  把每个非系统列平铺成 field，summary 在这里以 **Markdown** 第二次渲染。
- 表卡 `catalog.js`：第 174 行 `row_semantics || purpose`，所以每张卡正中都是「一行是…」。
  `||` 是死代码——`validateRows` 强制 table 的 `row_semantics` 非空——它掩盖的正是两个
  字段被塞进一个槽位。
- 表详情页 `catalog.js` 第 386 行 heading 已经用 `purpose`，与卡片不一致。
- 语义索引文档节点 `routes.js` 第 477 行：标题下那行 `row_semantics` 小字。

## 改法（四处）

1. **Row 页**：h2 = title；主块 = summary 的 Markdown（只此一次）；其余列进属性区；
   删掉标题下的 `row_semantics`。
2. **表卡**：中间那句用 `purpose`。
3. **语义索引文档节点**：撤掉 `row_semantics` 小字。
4. **表详情页**：不动（已经一致）。

`row_semantics` 的措辞约定（「一行是…」）不改，它只在表详情页出现一次并带标签。

## 顺序：先写兜底，再动渲染

1. **先补 summary 缺失的兜底**：`memora.row-detail/v1` 的 `display.summary_column` 可以为空
   （`rows.js` 的 `validateDetail` 已允许空串），`routes.js` 对这种表有「这张表没有配置
   summary 字段」的文案，`rows.js` 没有。改造后这类表的正中主块会**空白**，而这是手测最难
   撞到的分支——先复用同一文案 + `display.fallback`。
2. 再改 Row 页文档块，然后统一表卡文案，最后撤 routes 那行小字。
3. 每改一处跑 `./scripts/ci.sh`。

## 坑

- Admin 是**完整性冻结**的 bundle：`internal/adminui/bundle.go` 的 `frozenAssets` 写死每个
  资源的 sha256 与 size，`internal/adminui/bundle_test.go`（635 行）断言资源集合与哈希。
  **每改一次 JS 就同步一次哈希与测试，别攒到最后。**
- `dist/` 里是手写 JS，仓库没有 TS 构建链；它既是源也是产物。
- 复用现成样式（`.row-document-layout`、`.semantic-document-*`），不新增布局体系。

## 待定

- 是否现在动手——**未授权**。
- 卡片不再提示行的粒度，信息量压到 `purpose` 的写作质量上；`me` 四张表现值合格
  （秋招投递与进展 / 实习与工作经历 / 当前有效的个人身份与求职意向 / 可对外陈述的关键项目），
  是否回头校一遍。
- 独立发现：`me.experiences` 的业务列 `role` 与 Catalog 的 `ROLE` 撞名，早晚会咬人。
