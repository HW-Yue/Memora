# Admin 与本地可观察性小 Feature 计划

状态：F116 已完成，下一项 F117；F110–F122 持续执行已获用户授权，仍逐项 Review、
测试与独立合入。

## 产品形态

普通用户得到类似 MySQL Admin 的本地界面，但语义内容不使用传统 Row Grid：

```text
Instance/Database/Table → Route Tree → leaf locator → RowID → 完整动态文档
                                             ↘ History / Relation / Source
commit timeline → changed objects → before/after
Route trace → 每层可见节点 → AI 选择/回退 → RowID
```

```text
memora binary → go:embed dist/ → 临时 loopback Gateway → Unix Socket
→ msql.execute → Parser → Policy → Executor → Page/B+ Tree
```

正式运行不依赖 Node.js、CDN、外部字体或遥测；Gateway 不读物理 Store，也不绕过
MSQL。第一版只读，使用固定 actor/scope、随机 token、Host/Origin、SameSite Cookie
和 CSRF 防护。

## 先冻结读取协议

| Feature | RED 先证明 | 唯一结果 |
| --- | --- | --- |
| F109 Change Log | rollback/crash tail 或半套 split 可见 | 已完成：一个 commit 对应完整变化 envelope |
| F110 Metadata Read | Admin 被迫读 Store 或无界列举 | 已完成：Database/Table/Schema 的有界 MSQL |
| F111 Route Read | Route 叶泄露正文或整树一次加载 | 已完成：节点/children/locator 分层 cursor |
| F112 Row Detail Read | 前端猜 `title/body` 或丢动态字段 | 已完成：Data Dictionary 驱动的 Row/History envelope |
| F113 Change Read | 已完成：固定 snapshot、Page locator、Database scope 的有界 MSQL |
| F114 Trace Read | 已完成：可清理、scope-safe 且不含 prompt/正文的 trace envelope |

所有列表必须返回 `limit/cursor/snapshot/truncated/version`。Row 只能由 `SELECT`
回表；Route 只返回节点或 locator。管理元数据可用表格，业务 Row 由展示角色决定
标题、摘要、字段顺序；缺失时显示 RowID/revision，不猜列名。

## 再建立本地交付面

### F115 Local Read API

- 已完成：`memora admin --scope ... --no-open` 启动临时只读 Gateway；
- 已验收：共享 read-only classifier、固定 scope、API/CLI envelope 等价、一次性 token、
  SameSite Cookie、Host/Origin/CSRF、故障注入与关闭后端口释放；
- 未包含：HTML、公网 API、登录、模型 Provider。

### F116 Embedded Admin Shell

- 已完成：`go:embed` HTML/JS/CSS、完整性 manifest、深链路壳、session 状态和统一
  MSQL client；默认打开浏览器，`--no-open` 保留；
- 已验收：CSP、资源 hash、Cookie session 刷新恢复、真实 binary/Chrome smoke 和
  双架构离线构建；
- 未包含：任何具体业务页面。

## 页面逐个交付

| Feature | RED 先证明 | 唯一页面结果 |
| --- | --- | --- |
| F117 Catalog Navigation | 用户看不到库/表/结构层级 | Instance→Database→Table→Schema 浏览 |
| F118 Route Tree Browser | 语义索引只能看扁平 JSON | 按层展开 Route 并显示 locator |
| F119 Row Document View | 千字 Row 被塞进横向表格 | locator→RowID→完整动态文档详情 |
| F120 Change Timeline | 用户看不到提交顺序和影响对象 | commit 级变化时间线 |
| F121 Revision Diff | 用户无法比较变化前后 | 一个 Row/Route revision 的 before/after |
| F122 Route Trace View | 只能看到最终 RowID | 每层节点、选择、回退、耗时与预算叠加树 |

每个页面单独覆盖 loading、empty、error、truncated、permission 和 corrupt；使用真实
Gateway browser journey，不以 mock 页面截图代替。F117 不顺手做 Route，F118 不
顺手做 Row 详情，F120 不顺手做 diff。

## 待逐项 Review

- 命令最终使用 `admin` 还是 `studio`；
- F120 Change Log 默认保留窗口；
- F121 正文 diff 的字节预算；
- F122 trace 保存期限与脱敏字段。
