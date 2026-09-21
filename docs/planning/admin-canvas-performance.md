# Admin 语义画布的性能

状态：已改（2026-09-21）。用户实测"语义树的界面很卡顿"后定位并修复。

## 症状与数据

- 症状：`/routes/<db>/<table>` 的无限画布在平移/缩放时掉帧，展开节点时也有一次卡顿。
- 真实数据很小，所以不是"树太大"：

| 表 | 语义节点 | 行 | 正文最长 |
|---|---|---|---|
| profile | 2 | 1 | 498 字 |
| applications | 2 | 1 | 145 字 |
| experiences | 4 | 2 | 457 字 |
| projects | 8 | 5 | 499 字 |
| modules | 25 | 19 | 506 字 |
| decisions | 20 | 15 | 477 字 |

## 病因（代码级）

1. **画布上的"文档卡"是 DOM**：G6 的 `html` 类型节点，900px 宽，`innerHTML` 是整篇 Markdown 的
   渲染结果。查过 vendored 的 G6 5.1.1：相机变化只在 `endFrame` 里改 `$camera.style.transform`，
   **不**逐帧重建节点 DOM；但每个 HTML 节点在 `getOrCreateEl` 里被写上
   `style.will-change = "transform"`，于是每张卡都是独立合成层。900px 的文字块 + 72px 模糊半径的
   `box-shadow`，在缩放时要整块重新栅格化。
2. **自定义手势桥逐事件调用**：容器 capture 阶段监听 `pointermove` / `wheel`，**每个事件**各调一次
   `graph.translateBy` / `graph.zoomBy`。触控板 pointermove 可达 120Hz；两指滚动是一串 wheel。
   拖背景走 G6 自带 `drag-canvas`、拖卡片走这条自定义路径——两套实现，手感与开销都不同。
3. **手写的逐帧淡入**：`localMotionController` 在 140ms 内每帧 `updateNodeData` + `await graph.draw()`
   （`graphData` 里用 `motionOpacity` 把新节点先设成 0）。`draw()` 会触发 HTML 节点的属性更新，
   等于每帧重写这些卡片。
4. **每次展开/收起是整树重建**：`setData(graphData(tree))` + `await graph.render()` +
   每张卡一次 `translateElementTo`（`alignDocumentColumn`）。
5. **长节点名溢出**（用户截图）：节点宽固定 220px，`Admin 显示槽位：文档居中且只渲染一次`
   这类 Agent 写的名字直接跑出边框、压到相邻节点上。

## 这次改了什么

- **手势按帧合并**：`pendingPan` / `pendingZoom` 只累加增量，`requestAnimationFrame` 每帧最多应用
  一次；`pointerup` 时立刻 flush（最后一小段位移不能等一个不会到来的帧）。容器已卸载则丢弃。
- **卡片画得便宜**：`box-shadow: 0 28px 72px` → `0 4px 14px`；去掉 `.semantic-document-enter` 的
  `will-change: transform, opacity`；卡片加 `contain: paint`。
- **删掉逐帧淡入**（连 `motionOpacity` 数据管道一起删；只删动画会让新分支永久透明）。
  卡片淡入仍由已有的 CSS `@keyframes semantic-document-enter` 负责，分支矩形直接出现。
- **节点名自适应**：`routeNodeLayout(name)` 用与画布相同的字体（canvas `measureText`）按字测量并
  折行到最多两行，盒子宽度取 `[220, 360]`，高度随行数；G6 用
  `labelWordWrap` / `labelWordWrapWidth` / `labelMaxLines` / `labelTextOverflow` 折行，
  布局的 `getWidth` / `getHeight` 读**同一份**结果，边线因此仍然落在盒子上。

## 明确不做（以及为什么）

- **`content-visibility: auto` / `contain-intrinsic-size`**：19 张 ~500 字的卡片收益有限，而
  内在尺寸写错会让卡片先按错误高度显示；离屏测量探针也可能被它影响。
- **手势中把卡片降级成占位（LOD）**：两指平移只有 `wheel`、没有"手势结束"事件，计时器收尾会抖；
  `visibility: hidden` 还会让卡片内的选区与焦点失效。
- **展开/收起改成 `addData` / `removeData`**：G6 5.1 的树结构来自节点数据的 `children`
  （`updateNodeLikeHierarchy`），只加一个节点和一条边会让它变成第二棵根，`compact-box` 布局随之
  错位——不等价，且在 25 个节点这个规模上没有回报。

## 未验证的部分

本机 Chrome 的 OpenCLI Browser Bridge 扩展没有连上，**没有采到帧时间**：上面是代码级证据加真实
数据量得出的判断。如果之后能采样，优先看"缩放时每帧耗时"与"展开一次的总耗时"；
若仍不满意，下一个杠杆是 LOD（先做手势结束的判定再上）。
