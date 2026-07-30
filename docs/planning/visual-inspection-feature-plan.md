# 数据可视化与本地观察接口计划

状态：讨论稿，待用户 Review；候选 F81–F84 未获执行授权。

## 候选用户故事

`US-OBSERVE`：作为普通用户，我能直接看到 Memora 中有哪些 Database、Table、
Schema、Row、关系、历史、来源和语义 Route Tree；我能看见 AI 怎样沿 Route 找到
RowID 以及最终执行的 SQL，而不需要读取物理文件或理解存储引擎。

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

## 用户可见页面

1. Instance：版本、健康状态、Database 数、当前配置和有限审计摘要；
2. Database/Table：purpose、scope、anti-scope、Schema、alias 和版本；
3. Row 浏览器：稳定分页、字段、RowID、revision、state 和 commit sequence；
4. Row 详情：History、关系、来源强度、Source Receipt 定位和补偿链；
5. Route Tree：从 Table root 懒加载子节点，显示 purpose/synopsis/revision；
6. Route 叶子：只显示 locator，再显式 `SELECT` 回表展示 Row；
7. Route Trace：显示 AI 每层看到什么、选择什么、最终 RowID 和结果状态；
8. Health：结构问题、导航失败热点和待 Review 的维护计划，不静默修复。

UI 不一次下载整库或整棵树。所有列表、子节点、History、关系和 Row 都必须有
limit、cursor、truncated 与版本信息。

## F81 Inspection MSQL Read Model

目标：先补齐“可安全浏览”的确定性读协议，UI 不拥有特殊后门。

- 为 Database、Table 和 Row 增加稳定 cursor 分页；
- 为 Row 浏览冻结 `row_id` 顺序与 next cursor，不用无限 OFFSET；
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

- 单页本地应用，完成 Instance → Database → Table → Row 的浏览；
- Route Tree 懒加载、节点详情、叶子 locator 与 RowID 回表；
- Row History、关系、来源与配置联动展示；
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

1. 第一版是否确认采用本地浏览器，而不是原生 macOS App；
2. 第一版是否严格只读，任何编辑能力都后置；
3. HTTP API 是否作为稳定本地接口对第三方开放，还是先标记 experimental。

