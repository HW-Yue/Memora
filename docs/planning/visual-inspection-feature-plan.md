# 数据可视化与本地观察接口计划

状态：讨论稿，待用户 Review；候选 F81–F84 未获执行授权。

## 候选用户故事

`US-OBSERVE`：作为普通用户，我能在类似 MySQL Admin 的管理界面中看到
Memora 的 Database、Table、Schema、关系、历史、来源和健康状态；数据浏览
以语义 Route Tree 为主入口，点开叶子后能阅读约 1000 字的完整语义数据项
及其字段结构，而不是在横向表格中查看长文本 Row。

批准后再把该故事提升进产品宪章。

## 推荐产品形态

```text
浏览器 / 本地第三方工具
  → memora studio（按需、前台、127.0.0.1 随机端口）
    → 版本化只读 HTTP/JSON API
      → 当前用户 Unix Socket
        → msql.execute / 现有版本化读协议
          → Parser → Policy → Executor → 原生 authority
```

- daemon 默认不增加常驻 HTTP 监听；
- Studio Gateway 不读取 `.memora` 文件，不调用 Store 私有 API；
- 数据 Row 必须由 `SELECT` 返回，Route 只返回节点或 locator；
- 浏览器不能提交写 AST；第一版没有“编辑”“修复”“重建索引”按钮；
- 启动时固定 actor 与 Database scope，浏览器请求不能自行扩大；
- 只绑定 loopback，校验 Host/Origin，使用随机会话令牌、SameSite Cookie 和 CSRF；
- 静态资源嵌入发行二进制，不使用 CDN、遥测或外部字体；
- 每个视图可展开“实际 MSQL”，让用户知道页面数据如何取得。

## Admin 信息架构

Memora Studio 参考常规 MySQL Admin 的管理层级，但不照搬“Row Grid 是主视图”：

```text
左侧：Instance / Database / Table 导航
中间：Table 语义 Route Tree，懒加载当前层
右侧：叶子数据项列表或选中数据项的完整文档详情
底部抽屉：RowID / revision / History / Relation / Source / 实际 MSQL
```

- 首页和管理页可以使用表格展示 Database、Table、Column、配置和健康问题；
- 业务数据项默认使用文档详情视图，不使用横向 Row Grid；
- 文档视图按 Data Dictionary 渲染动态字段，保留标题、正文、状态、时间、列表和关系等结构；
- 提供“原始字段”折叠区供调试，但不将 JSON 或数据表作为默认阅读体验；
- 一个叶子有多个 locator 时，先显示紧凑数据项卡片；点开卡片后显示完整数据项；
- 叶子自身不返回正文。Studio 必须展示 `OPEN ROUTE → RowID → SELECT` 两步读取链路。

## 数据项展示契约

Table 由 AI 动态建模，前端不能假设每张表都有同名的 `title/body`。F81 需要在
Data Dictionary 中增加可选、版本化的展示元数据：

- 数据项标题 Column；
- 卡片摘要 Column 与最大预览字符；
- 详情字段顺序和类型展示角色；
- Table 无展示元数据时的确定性降级：卡片只显示 RowID/revision，不按列名猜标题；
- 完整详情始终显示全部业务字段，展示元数据不得隐藏数据。

卡片和完整详情都必须由有界 `SELECT` 生成。卡片只查展示字段，点开后再
查完整字段，不因为一个叶子含多个约 1000 字数据项就一次载入全部正文。

## 用户可见页面

1. Instance：版本、健康状态、Database 数、当前配置和有限审计摘要；
2. Database/Table：purpose、scope、anti-scope、Schema、alias 和版本；
3. 数据项浏览：按 Route 叶子显示紧凑卡片，不用数据表承载长文本；
4. 数据项详情：完整动态字段、RowID、revision、History、关系、来源和补偿链；
5. Route Tree：从 Table root 懒加载子节点，显示 purpose/synopsis/revision；
6. Route 叶子：只显示 locator，再显式 `SELECT` 回表展示 Row；
7. Route Trace：显示 AI 每层看到什么、选择什么、最终 RowID 和结果状态；
8. Health：结构问题、导航失败热点和待 Review 的维护计划，不静默修复。

UI 不一次下载整库或整棵树。所有列表、子节点、History、关系和 Row 都必须有
limit、cursor、truncated 与版本信息。

## F81 Inspection MSQL Read Model

目标：先补齐“可安全浏览”的确定性读协议，UI 不拥有特殊后门。

- 为 Database、Table 和语义数据项增加稳定 cursor 分页；
- 为叶子中的 locator/数据项冻结 `row_id` 顺序与 next cursor，不用无限 OFFSET；
- 冻结 Data Dictionary 的数据项标题、卡片摘要、字段顺序和确定性降级契约；
- 统一暴露 History、Relation、Source Receipt、Health 和 Instance 摘要的有界读取；
- 所有结果继续使用 `memora.result/v1` 或显式版本化 envelope；
- 验收：10k Row、深 Route、多 History 均可逐页浏览，单次响应不超预算。

不做：HTTP、HTML、写入、全文导出和物理 Page 观察。

## F82 Local Read API

目标：暴露可供 Studio 和本地工具复用的版本化接口。

- `memora studio --scope ... --no-open` 启动临时 loopback Gateway；
- API 只接受读 MSQL、参数和 cursor；Gateway 注入固定 Authorization；
- 复用 daemon Unix Socket 和 Batch Session，不建立第二套执行器；
- 返回原始 Result Envelope、request ID、耗时、响应字节和 truncation；
- 拒绝 mutation、跨 scope、非 loopback bind、跨 Origin 和超预算请求；
- 验收：API 与直接 CLI 的 MSQL/envelope 等价，安全故障注入全部通过。

不做：公网 API、远程登录、API Key、写接口和模型 Provider。

## F83 Memora Studio v1

目标：让普通用户第一次真实看到数据库内容和语义索引结构。

- 类 MySQL Admin 的本地管理应用，完成 Instance → Database → Table 管理导航；
- Route Tree 是数据浏览主入口，支持懒加载、节点详情、叶子 locator 与 RowID 回表；
- 叶子下先显示数据项卡片，再显示约 1000 字完整文档和动态字段结构；
- History、关系、来源、配置与原始字段作为详情面板；
- 空、截断、权限拒绝、陈旧 revision 和损坏状态显式可见；
- 每个面板显示来源 ID/revision，并可复制实际 MSQL；
- 验收：仅通过发行二进制和本地 API，在干净 Instance 完成完整观察旅程。

不做：图形化写入、拖动 Route 节点、聊天问答和自动优化。

## F84 Route Trace 与可视化诊断

目标：不只显示静态树，还显示 AI 实际怎样使用它。

- 宿主可提交版本化、可选的 Route Trace Receipt；
- 只记录节点 ID/revision、选择、RowID、调用数、字节、耗时和结果状态；
- 默认不保存原始 prompt、正文、模型隐藏推理或 Provider 凭据；
- Studio 将 trace 覆盖在树上，显示错误分支、回退、空叶子和高成本路径；
- trace 是可清理的观察数据，不是语义事实，不进入 Wiki 或 Database package；
- 验收：Codex/Claude 各完成一条真实路线，Studio 可复现路径且不能泄露正文。

该 Feature 为后续 Router Health、真实 benchmark 和局部优化提供证据。

## 待 Review 的三个产品选择

1. 第一版是否严格只读，任何编辑能力都后置；
2. HTTP API 是否作为稳定本地接口对第三方开放，还是先标记 experimental；
3. 叶子下有多个数据项时，是否确认“卡片列表 → 完整文档详情”的两层交互。
