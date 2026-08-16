# Admin Semantic Local Motion v1

状态：已批准实现（F216，局部动画实验）。

## 目标

Route Tree 保持确定、低延迟的布局更新，同时让用户能感知 Branch 首次加载和 Leaf
文档接入的连续性。动画只作用于新内容，不改变布局算法、节点尺寸、当前缩放或拖拽语义。

语义索引详情页是独立的全屏画布工作区：进入具体 Table 的 Route Tree 后隐藏 Admin
导航、会话卡片和重复的页面说明，只在画布上保留返回表与聚焦到中心两个控制。

## 不变量

- G6 全局、布局和 `collapse-expand` 动画继续为 `false`；树的重新布局必须一次完成。
- `autoFit` 只在首次渲染和用户点击“聚焦到中心”时执行；增量加载不得自动缩小整棵树。
- 同时展开多个 Row 时，每次重排后把全部文档统一对齐到 Route 节点右侧同一列，并按
  实际文档高度保留纵向间距；禁止只移动最新文档而让旧文档沿用布局器坐标。
- 首次聚焦和“聚焦到中心”设置温和的最大缩放比例；Leaf 打开 Row 后聚焦该 Row，
  收起 Row 后焦点回到原 Leaf，不跳回整棵树中心。
- Branch 加载后新节点只做短暂透明度渐入；边可以同步出现，不为动画增加二次布局。
- Leaf 文档在最终 Row 内容接入后，由 HTML 内层做一次 `opacity + translate/scale`
  合成动画；外层 G6 HTML 节点不设置 CSS transform，避免和画布定位冲突。
- 同一画布的新动画必须可被后一次更新中断；不允许旧的 `requestAnimationFrame` 在新内容
  后继续修改图形。
- 动画预算：Branch ≤ 160ms，文档 ≤ 200ms；动画帧只调用 `draw()`，不调用 `render()`。
- `prefers-reduced-motion: reduce` 时完全跳过动画；所有状态仍在一次同步更新后可见。
- 拖拽、两指平移、捏合缩放和 Option 文字选择不经过动画调度器。

## 实现边界

使用公开 G6 数据更新与 `draw()`，不依赖内部 scenegraph API。动画状态只存在于前端
Route Tree 的临时 graph data，不写入数据库或语义树。失败、取消和页面切换都必须失效
当前 token，并取消 pending frame。

## 验收证据

测试冻结上述字符串和 reduced-motion CSS；隔离 Chrome 验证 Branch/Leaf 的最终节点数、
无重复请求、交互不阻塞，并采集动画期间 `draw()` p95 与长任务数量。若局部 RAF 超过预算，
只保留文档 CSS 动画并记录降级，不重新打开 G6 全局动画。
