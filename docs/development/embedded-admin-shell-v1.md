# Embedded Admin Shell v1

状态：F116 已完成并验收；Admin 语义画布 Feature 在此基础上演进。

## 用户结果

`memora admin` 启动 F115 Gateway，以全库只读模式展示当前 Instance 的 Database
Catalog，并用系统浏览器打开离线 Admin 壳。`--scope DATABASE` 是可选过滤器，
`--no-open` 仍可只打印 session descriptor。HTML、CSS、JavaScript 全部通过
`go:embed` 编译进同一个 `memora` binary，不需要 Node.js、CDN、外部字体或网络。

F116 只交付导航容器、session 状态和统一 MSQL client。Catalog、Route、Row、Change、
Diff 与 Trace 页面逐项实现；壳不得猜测或读取业务 Row。当前 Route 页面使用本地 G6
无限画布，Leaf 预览仍然通过 `OPEN ROUTE` 和 RowID `SELECT` 回表。

## Bundle 与路由

内嵌 bundle 的文件集合、大小和 SHA-256 在 Go manifest 中冻结。启动前必须验证
index/JS/CSS 全部存在且逐字节匹配；缺失、增加或 tamper 都拒绝启动，不从磁盘或网络
回退。HTML 使用 `no-store`；稳定 asset URL 返回强 ETag 与 `no-cache`，每次使用前必须
重新验证。bundle generation 同时进入 HTML 入口和 ES module import URL，升级二进制后
不能继续执行旧版本标记为 immutable 的缓存脚本。

`GET /` 和不属于 `/api/`、`/assets/` 的深链路返回同一 `index.html`；已知 asset
按正确 MIME 返回，未知 asset 与 API 路径为 404。只支持 GET/HEAD，不把 POST
深链路误路由到 HTML。

各页面对 MSQL 结果执行版本化的严格字段校验；当后端读取契约增加必填字段时，前端
validator 与 frozen bundle 必须在同一 Feature 中同步升级。Route Tree 接受并校验
F182a 定义的非 null `aliases` 字段（最多 8 项、单项 1–64 个 Unicode 字符、合计最多
512 UTF-8 bytes）。这类协议扩展不要求迁移或删除已有数据库。

Route Canvas 默认从左到右显示 Table 语义树。进入页面只读取根和第一层节点；点击
Branch 才读取下一层。点击 Leaf 先打开唯一 locator，并按 RowID 回表，将 Row 的全部字段
内容作为 Leaf 后方的真实大型 document 节点接入同一棵 G6 树；该节点参与布局、连线、拖动
和缩放，不使用浮窗、弹出卡片或跳转页面；画布节点本身展示全部系统字段和业务字段，用户
不需要再打开其他页面才能读完 Row。再次点击 Leaf 会移除这个 document 节点。已有 Row
深链路只保留为独立的 History/Revision 观察入口，不参与 Route Tree 的读取交互。画布工具栏
的“聚焦到中心”只调整当前已加载节点的视口，不重新请求或改变语义树状态。
增量加载不会触发 `autoFit`；当前缩放和视口位置保持不变，只有首次进入或用户主动聚焦时
才重新适配画布。

当前 document 节点使用 G6 `html` 节点承载一个固定阅读宽度约 900px 的 WPS 风格文档面：
标题、Row 语义说明和系统元数据位于页眉，`summary` 作为主要 Markdown 正文，其余业务字段
位于同一节点底部的“记录字段”区。Markdown 由本地 `markdown-it` 解析、`DOMPurify` 清理后再
挂入 HTML，支持标题、加粗、引用、列表、代码块和表格；外部 Row 内容不会直接拼接成未经清理
的 HTML。节点高度按渲染后的 DOM 测量后交给布局，避免 Canvas 把长文压成一段不可读的纯文本。

画布导航遵循触控板优先的交互契约：普通滚轮/两指滑动由 `scroll-canvas` 平移视口，带有
`ctrlKey` 或 `metaKey` 的捏合事件才由 `zoom-canvas` 缩放；鼠标拖动仍可作为备用路径。
document HTML 节点默认禁止文字选择，普通拖动直接平移画布；按住
`Option/Alt` 才临时开启文字选择。所有这些事件只在画布容器内拦截，不影响页面其他区域的
滚动和浏览器默认行为。

节点点击必须形成单一、短促的视觉事务。首次点击尚未加载的 Branch 时，内置
`collapse-expand` 不得提前执行；子节点读取完成后只做一次整树更新并保持展开。点击 Leaf
时先完成 locator 与 Row 回表，再把最终 document（或最终错误/空状态）一次接入树中，不用
临时 document 节点触发第二次布局。数据更新与布局不做元素动画，避免整棵树追逐旧位置；最终
document 只执行一次短视口聚焦，禁止两轮视口动画互相覆盖；元素布局本身不得再做过渡动画。

F216 在上述边界内增加局部微动画：新 Branch 节点使用不超过 160ms 的透明度渐入，最终
Row 文档由 HTML 内层使用不超过 200ms 的合成属性动画接入；动画可被后一次更新取消，且
`prefers-reduced-motion` 会完全跳过。详细契约见
[Admin Semantic Local Motion v1](./admin-semantic-local-motion-v1.md)。

## Browser session

启动 URL 仍把 bootstrap token 放在 fragment。模块脚本首先同步调用
`history.replaceState` 清除 fragment，再用 Bearer token POST `/api/v1/session`。页面
刷新或深链路重载时，可凭仍有效的 SameSite Cookie 和精确 Origin 恢复同一临时
session 的 CSRF；一次性 bootstrap token 仍不可重放。CSRF token 只保存在 JavaScript 模块内存，禁止 localStorage、sessionStorage、URL、
DOM 或日志持久化。统一 `executeMSQL(source, statements)` client 只调用同源
`/api/v1/msql`，设置 Cookie credentials 与 CSRF header；401 会清除内存 session 并
进入 expired 状态。

壳明确显示 bootstrapping、ready、error、expired 四种状态。错误文案不显示 token、
Cookie、参数、Row 或 daemon 内部错误。

## Web 安全与可移植性

HTML 无 inline script/style，Gateway 对壳资源设置只允许 `self` 的 CSP，并禁止
object、base、frame、form；同时设置 `no-referrer`、`nosniff`、deny framing 和最小
Permissions-Policy。正式构建仍是 CGO-disabled 的 darwin/arm64 与 darwin/amd64
单文件二进制；资源不得依赖运行时工作目录。
