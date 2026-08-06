# Memora 产品手册与架构说明

本文件是给 Agent 按需加载的产品参考，不是动态 Database、Schema、Route 或 Row 的来源。
动态状态只能通过当前 Instance 返回的 MSQL Result 获取。

## 目录

- [产品定位](#产品定位)
- [整体架构](#整体架构)
- [读取流程](#读取流程)
- [写入与资料吸收](#写入与资料吸收)
- [Admin 观察面](#admin-观察面)
- [安装、运行与故障处理](#安装运行与故障处理)
- [边界与当前版本](#边界与当前版本)

## 产品定位

Memora 是本地、面向 AI Agent 的个人语义数据库。AI 是逻辑层的首要用户：它负责
判断知识的语义、设计 Database/Table/Column、选择查询路径和提出维护计划；Memora
引擎负责权限、类型、约束、事务、并发、版本、索引、恢复和物理存储。

持久化的最小产品单位是可独立修改的完整语义 Row，而不是机械文档 chunk、聊天转录、
Embedding 或原始 PDF/图片。外部资料只在宿主侧临时读取，经过覆盖、来源锚点和复核后，
写入可维护的语义模块，并用 Source Receipt 表示已完成吸收。

MSQL 是 Agent 的唯一正式数据库语言（对外说明时也可简称 SQL）。Agent 不直接操作
Page、B+ Tree、Buffer Pool、WAL、Undo/Redo、倒排文件或 Instance 文件。

## 整体架构

```text
用户 / 外部 Agent / 内置编排器
          │  自然语言、资料、授权
          ▼
宿主 Skill（发现、查询、写入、复核）
          │  MSQL / memora.result/v1
          ├──────── MCP stdio（memora_execute）
          └──────── CLI / Go SDK
                       │ Unix socket（本机 daemon）
                       ▼
Memora daemon
  ├─ MSQL Lexer / Parser / Binder
  ├─ Authorization + Policy（L0 读、L1 有界写、L2 结构变更）
  ├─ Statement / Mutation Plan / Transaction Executor
  ├─ Logical authority
  │    ├─ Database → Table → Column / Row / Row History
  │    ├─ Relation
  │    └─ Table Route：Branch → Leaf → 0..1 RowID
  ├─ Derived navigation indexes
  │    ├─ semantic Route index（逐层导航）
  │    ├─ full-content lexical postings（候选位置）
  │    └─ optional Route-only vector predictor（只作提示）
  └─ Native storage
       ├─ 16 KiB Page + Buffer Pool + persistent B+ Tree
       ├─ MVCC / object locks / expected revision
       ├─ Redo WAL + checkpoint + crash recovery
       └─ committed Change Log（可审计变化流）
```

Route、lexical 和 vector 都是导航层。它们返回候选位置或 RowID，不能直接返回事实；
最终答案必须来自 revision 匹配的 `SELECT`。一个 Leaf 最多挂一个活跃 Row，同一个
Row 可以挂在多个语义 Leaf；正文只保存一份。

模型 Provider 属于宿主，不属于 Memora。API key、base URL 和完整模型上下文不能写入
数据库、日志、收据或 MSQL input。当前 Skill-first 产品不要求 Memora 自带模型；
内置 Agent/评测编排是独立的后续模块。

## 读取流程

```text
检查安装 → 确认 daemon → 绑定授权 scope
  → SHOW CATALOG ATLAS（必要时继续 cursor）
  → 选 Database/Table 与 Schema
  → lexical / optional vector 预测候选（可跳过）
  → SHOW ROUTES 根节点
  → 每次只选一层并读取下一层
  → OPEN ROUTE（得到唯一 Row locator）
  → SELECT RowID + projection + revision
  → 只根据 SELECT 事实回答并引用来源
```

候选预测失败、零命中、过期或缺少向量编码器都不是事实查询失败；回到确定性 Route
导航。每个 query 使用有界 Route Frame，不把动态索引写入长期 system prompt，也不把
整个目录或全文塞进上下文。发生 revision 冲突时丢弃旧 Frame，刷新一次并重新读取。

## 写入与资料吸收

短文本或对话陈述按以下顺序处理：

```text
capture pending → 发现现有 Row → IGNORE / INSERT / REVISE / MERGE / SPLIT / MOVE / RELATE
→ 生成 hash-bound Mutation Plan → Policy preflight → 短事务 MSQL 提交
→ SELECT 回读 → 检查 Row revision、Relation、Route membership → decide / receipt
```

所有写入都必须带 expected schema/revision、授权 scope、最大影响行数和完整 Route
membership snapshot。已占用 Leaf 不能再挂第二个 Row；需要新语义叶或局部 Branch 调整。
语义冲突必须展示证据并请求用户裁决，不能由数据库或 Agent 静默选边。

长文档、EPUB、DOCX、文本层 PDF 或 OCR 资料走 Assimilation：宿主解析为有序的临时
Document IR，保存 source locator、coverage、window checksum 和 checkpoint；按窗口
阅读并写入完整语义模块。`coverage_complete` 只表示读完，只有独立复核后得到
`committed Source Receipt` 才表示真正写入成功。不要把原文、机械 chunk 或 OCR 权重写进
Memora。

## Admin 观察面

Admin 是同一个 `memora` 可执行文件内嵌的、只绑定本机 loopback 的临时只读观察界面，
不是第二个数据库、不是写入 API，也不是 Agent 的事实来源。启动前必须先让目标 daemon
正常运行：

```sh
memora daemon status --data-dir /absolute/instance
memora daemon start --data-dir /absolute/instance
memora admin --scope work --data-dir /absolute/instance
```

Admin 默认打开系统浏览器，固定监听 `127.0.0.1:3888`。不希望自动打开浏览器时使用：

```sh
memora admin --scope work --data-dir /absolute/instance --no-open
```

`--scope` 是启动时固定的 Database 白名单，每条 MSQL 都被绑定到该 scope。页面可观察
Catalog、Table/Schema、语义 Route、Row、History、Relation、Change 和 Route Trace；
它不能执行 INSERT/UPDATE/DELETE、Schema 变更、Route 维护、repair 或读取物理文件。
所有正式修改仍通过 Skill 的 MSQL Mutation Plan 完成。

Admin Gateway 只在 `memora admin` 进程运行期间占用额外资源；停止该命令即可释放
3888 端口，daemon 仍可独立运行。页面使用短期 session、同源 Cookie 和内存 CSRF token，
不把 token 放进 localStorage、URL 或日志；HTML/JS/CSS 已编译进 binary，不依赖 Node、CDN
或外网。

若页面提示无法建立本地会话，先关闭当前 Admin，再确认同一 `--data-dir` 的 daemon 已启动，
重新运行 `memora admin`。若提示暂时无法读取 Route Tree，先运行 `memora doctor` 检查
Instance，再用同一 Database scope 重试；不要通过修改文件或重建索引来绕过页面错误。

## 安装、运行与故障处理

独立 Skill 仓库：<https://github.com/HW-Yue/memora-skill>。
当前 Release：<https://github.com/HW-Yue/Memora/releases/tag/v0.1.0>。

每次首次使用先执行 `scripts/check.sh`：

- `ready`：使用检测到的 `memora`，不重复安装；
- `missing`：向用户展示 Release 地址和默认安装位置，等待用户授权；
- `unhealthy`：展示有限诊断，等待用户确认后才允许替换。

授权后才可执行 `scripts/install.sh --yes`。安装器只支持 macOS arm64/amd64，固定版本、
通过 HTTPS 下载并验证 checksum 与 `memora version --json`，不请求 sudo。安装后必须完成
init、daemon start 和 doctor 检查，才能向用户报告安装成功。

常用入口：

```sh
memora init --instance work
memora daemon start --data-dir /absolute/instance
memora doctor --data-dir /absolute/instance
memora query --input '{...authorization...}' 'SHOW CATALOG ATLAS LIMIT 64'
memora exec  --input '{...authorization...}' 'SELECT ...'
```

升级和 `doctor repair` 必须先展示只读计划并获得单独确认；安装同意不等于升级或修复同意。
遇到 `permission_denied`、`stale_revision`、checksum 不一致、签名不匹配或 `in_doubt`，
停止扩权和盲目重试，按返回的逻辑收据重新发现或请求用户处理。

## 边界与当前版本

- v0.1.0 提供 macOS arm64/amd64 制品、daemon、CLI、MCP、Skill、语义 Router、lexical
  postings、事务历史、Admin 和恢复基础设施。
- HNSW、Apple Accelerate、复制、PITR、多设备同步和大规模真实质量评测尚未作为默认能力。
- Admin 的可视化不能替代外部 Agent 的答案质量测评；Recall/MRR 必须使用独立 evaluator。
- 任何不确定的事实都回到当前 Instance 的 MSQL 结果，不从本手册或旧会话推断动态状态。
