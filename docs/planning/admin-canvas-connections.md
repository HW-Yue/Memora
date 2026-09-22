# Admin 语义画布的连线

状态：**第一刀已实现并真机验证（2026-09-23）**，第二刀（port 锚点）未做。对象是
`/routes/<db>/<table>` 上 Route ↔ 文档卡的那些边。

## 实现记录（2026-09-23）

分支 `fix/canvas-layout-eats-card-height`。改动落在 `internal/adminui/dist/assets/routes.js`：

- **尺寸表**：新增 `layoutNodeSizes` / `rememberLayoutSizes()` / `layoutNodeSize()`。`graphData()` 在
  建图前用同一棵树重建这张 `Map<id,{w,h}>`，布局回调按 **id** 查表，不再读 G6 递给它的节点参数。
- **删掉 `alignDocumentColumn`**、`DOCUMENT_COLUMN_GAP`、`DOCUMENT_VERTICAL_GAP` 与全局位移；
  `translateElementTo` / `graphPosition` 随之成为死代码，一并删除。
- **布局引擎从 compact-box 换成 dagre**（`antv-dagre` / `rankdir: LR` / `ranker: tight-tree` /
  `nodeSize: layoutNodeSize` / `ranksep: 36` / `nodesep: 24`）。这不是风格选择，是 compact-box 在
  **混合尺寸下根本不成立**，见下。

### compact-box 为什么不能用（本次新查实，推翻原「决定」第 1 条的乐观前提）

在页面里直接跑 vendored 的 `G6.CompactBoxLayout`（两节点：Route 220×72 → 卡片 900×527）实测：

- 它回给调用方的是**含 gap 的盒子的左上角**（`x=-254,y=-80` 与 `x=110,y=-307`，盒宽 364 / 1044），
  而 `YE` 收尾只做一次整体 `translate`，**从不做逐节点的半尺寸修正**；G6 渲染时却把节点位置当**中心**。
- 尺寸一致时这个错位对全树是同一个常量位移，看不见——这正是它以前"看着没问题"的原因；一旦卡片
  527 高、Route 72 高，每张卡就被画在自己那一行的左上方半个自身尺寸处：横向压住 Route，纵向偏高
  半个卡高。实测 `dy ≈ -(卡高/2 - 36.5)`，与症状完全对上。
- dagre 收 `nodeSize`（可逐节点给真实尺寸）并回**中心**坐标，G6 直接照画，不需要任何补偿 pass。
  它的 `ranksep`/`nodesep` 是单边的，屏幕上的间隙是两倍，所以 36/24 对应原本的 72px 节奏。

### 真机验证（projects 表，四张卡同开）

用 CDP/OpenCLI 在 `me.projects` 的画布上展开两级、点开四张卡，读 `getElementRenderBounds`：

| 卡高 | Route 右边 → 卡左边 | 两边中心 y 差 |
|---|---|---|
| 527 | 71 | 0 |
| 527 | 71 | 0 |
| 475 | 71 | 0 |
| 527 | 71 | 0 |

即卡片正好在 Route 右侧、留出 72px，且与 Route **纵向居中**；验证截图留在被 gitignore 的 `.dsh-tmp/canvas-dagre-layout.png`，不入库。
过程里的两次翻车也记下来：同路径只换 hash 的导航不会重载页面，SPA 会拿着上一个 server 的 CSRF
打 401；画布变宽后节点会跑到视口外，合成点击前要先 `fitView`。

## 症状（用户原话）

「这种线条和对应的图标连不上…我可能展开的比较多。」

## 病因（截图像素实测 + 代码，运行中的 daemon 与 HEAD 的 `internal/adminui` 无差异）

- 画布上并排着两种尺度：Route 节点 220–360 × 72（canvas `rect`）；文档节点是 `html` 类型的
  DOM 卡，900 宽 × 测量出的整篇文档高度（实测千级 px，决策文档约 2000px）。
- 竖向排版是**两套互不知道对方**的系统：树布局（`compact-box`，direction LR）只按 Route 自己的
  尺寸排——截图上相邻 Route 盒心距 143px、盒高 86px，比值 1.66 ≈ `(72 + 2×22) / 72`，即布局只
  用了「72 高 + 上下各 22 间隙」，完全没吃卡片高度；文档卡另由 `alignDocumentColumn` 按真实高度
  堆成一列（列左 = `routeRight + 72`，卡间 72），最后整列再按 `originalMean − adjustedMean`
  做一次全局位移。
- 边是 `cubic-horizontal` 且**没有 port**：两端落在节点中心，从盒子边框穿出的位置由「两中心连线」
  与边框求交决定，所以落点随机；节点画在边之上，文档卡是 DOM 层（G6 的 `g-canvas-camera`）整块
  盖在 canvas 边线之上，线穿进卡片左边框后整段不可见。
- 净效果：72px 的小盒子和千级 px 的卡片被放在竖向相距千级 px 的位置，每条边都是长斜线——从盒子
  边缘随机位置钻出、被卡片吃掉、再在某张卡片的左边框随机高度露头。展开越多，卡片列越高、全局位移
  越大，越乱。
- 布局尺寸**确定**没吃到真实值（2026-09-22 深夜，读 vendored G6 5.1.1 源码坐实）：treeLayout 把
  布局节点映射成 `{id: idOf(n), data: n.data || {}}`（`g6-5.1.1.min.js` 的 `treeLayout`），而本项目
  节点数据是扁平的（`treeToGraphData` 原样返回用户对象；可证：style 回调里 `data.kind`/`data.name`
  有效，否则卡片不会变白、标签不会出现），扁平对象没有 `.data` 字段 → 布局拿到的 `data` 恒为 `{}`，
  于是 `layoutNodeData()` 返回 `{}`，`getWidth`/`getHeight` 对**所有**节点都返回 Route 尺度的
  220×72，文档卡的真实高度从未进入布局。这是「斜线越长越乱」的第一因，不是疑点。

## 决定（2026-09-22，顾问判词）

1. **让树布局吃真实高度，删掉 `alignDocumentColumn` 与全局位移。** LR 下 `getHeight` 就是纵向占位；
   文档节点返回实测卡高，compact-box 自己会把 Route 和它的卡片对成一行，斜线变近水平线，
   「越展开越乱」这个放大器直接消失。
   **2026-09-23 修订**：前半句成立且已实现，后半句的前提不成立——compact-box 在混合尺寸下会把节点
   画错位置（见「实现记录」），所以承载「吃真实高度」的布局引擎换成了 dagre。目标不变，工具变了。
2. **尺寸不赌 `node.data`。** `getWidth` / `getHeight` 改成闭包查一张 `Map<id, {w,h}>`（展开时按 id
   写入实测值），绕开 `layoutNodeData()` 返回 `{}` 的不确定性。
3. **锚点用 port，形状仍留 `cubic-horizontal`。** Route `{placement:'right'}`、文档卡
   `{placement:'left'}`，边设 `sourcePort` / `targetPort`：锚点跟着 bbox 走，任何移动都不会退化成
   「中心连线求交」，而且线终止在卡片左边框**外**，DOM 层盖不住它。html 节点的 port 若不灵，再退到
   自定义 edge 自己按两个 bbox 算锚点——不要一上来就写 path。
4. **不要端口圆点、不要箭头、不要正交阶梯。** 一对一关系，箭头不增信息；正交阶梯是电路图观感，
   视觉重量翻倍。
5. `alignDocumentColumn` 一删，「`translateElementTo` 之后边留旧路径」这个问题自动不存在——
   不赌 G6 内部会顺带重算关联边。

## 代价 / 放弃

- 卡片不再排成整齐一列；画布纵向总高由最高的卡片链决定，会变长。
- 保留「卡片单独一列 + 全局位移」= 留着放大器；正交走线、端口圆点/箭头这次都不做。

## 最大风险：测高时序

卡高要 DOM 渲染后才知道，布局却要在渲染前用 → 必然两趟（render → measure → relayout），会有一次
跳动。约束：卡片宽度锁死 900、高度按 doc id 缓存；每次展开只重排一次；宽度不要参与测量，否则变成
反馈循环。

**2026-09-22 深夜补充：这个风险比上面写的小。** 卡高在 `documentNode()` 里就用离屏探针测好了
（`measuredDocumentHeight`），而 `documentNode()` 在 `graphData()` 之前执行，所以尺寸表可以在
**建图那一趟同步填满**，不需要 render→measure→relayout。剩下的唯一前提是探针与画布同宽（都是
900，`.semantic-document-measure` 与 `.semantic-document-node` 同宽），否则折行不同、高度会差。

## 补问：锚点用 port 还是自定义 edge（2026-09-22 深夜，顾问判词）

问的是这一件：`html` 大卡片 + `rect` 小盒子、一对一父子关系、父左子右，锚点该用 G6 的 port，
还是自定义 edge 按两个 bbox 自己算。

**结论：用 port。自定义 edge 留作兜底，不要一上来就写。**

- **理由**：锚点语义是常量（Route 恒右中、卡片恒左中），正是 `placement` 的字面意思；自定义 edge
  等于把 `ky(bounds, placement)` 重新实现一遍，还得自己从 graph 反查 element 再取 bbox——html 节点
  这条路上更容易碰到「没挂载 / 没测完」的中间态。冻结包成本也差一个量级：port 是几行 style 配置 +
  边上两个字段，自定义 edge 是一个新类加一次 hash 重算，且以后 G6 内部 bounds 语义变了要自己跟。
- **放弃**：自适应锚点（一个 Route 挂多个子节点时 N 条边同点出发、扇不开；锚点也不能沿边滑动去缩短
  连线）；port 的交互身份（`r: 0` 之后它只是几何点，没有 hover / 命中区）；控制点级曲线调参（只能吃
  `cubic-horizontal` 的默认弯法）。
- **顾问强调的前提**：本文第 1、2 条（布局吃真实高度、删 `alignDocumentColumn`）不修，port 只会把
  「随机位置钻出」换成「精确地钻进卡片里」——**port 解决锚点，不解决坐标撕裂**。
- **最大风险 = html 卡片的 bounds 时序**：卡高来自离屏探针 + DOM 异步挂载，若 port 在真实尺寸落定前
  算过一次，目标锚点会停在错误的 y（甚至 0 高度处的中心），而且**不自愈**，要等下一次 update 才纠正。
  真机必须验的就这一点：展开一张千级 px 的卡，看它左侧锚点 y 是否正好是卡片的视觉中心；对不上先
  怀疑时序，别先怀疑 placement。

## 待定

- **第二刀：port 锚点（未做）。** 第一刀之后 Route→卡片已经是短近水平线、端点在卡片左边框的中点
  （真机实测 `dy = 0`、`gapX = 71`），所以 port 现在只解决剩下的两件事：一是分支 → 多个子 Route 的
  长斜线仍从盒子边框的"中心连线求交"位置穿出（同一父节点的多条边角度不同，锚点高度也就不同）；
  二是把一个"碰巧对齐"变成"结构上对齐"，将来任何移动都不会退化。
- ~~G6 5.1.1 的 `html` 节点上 port 能不能正常算 bbox~~（源码级判断：能。`Gb.render` 显式调用
  `drawPortShapes`；port 位置走 `key-container` 的 bounds，而 `key-container` 的 x/y/width/height
  就是 `getKeyStyle` 里的 `dx`/`dy` + 真实尺寸，所以 `dx = -w/2` 这种偏移不会漏算；`zB()` 在 port
  配置 `r` 为 0/缺省时直接用 `ky(keyShape.getBounds(), placement)` 兜底。**仍待真机一次确认**：
  把 port 的圆点藏掉（`r: 0`）之后边是否仍落在卡片左边框，且 DOM 层不再吃掉线头。顾问判词见上一节：
  这一处用 port，不写自定义 edge。）
- 锚点时序：见上一节「最大风险」，先真机看一张长卡的左侧锚点 y，再决定要不要在 port 之前先等一次
  卡片尺寸落定。（第一刀的实测里它没出现：四张卡的 `dy` 都是 0。）
- 两趟渲染的那次跳动怎么收——见「最大风险：测高时序」，第一刀里确认不存在（尺寸在建图前就测好了）。
- 卡片在画布上要不要收成摘要预览（这次不做，见「代价」）。
