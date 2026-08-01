# Phase A：契约、测试骨架与本地运行时

目标：得到一个可重复测试的 macOS Go 工程，CLI 能连接 daemon 并执行最小 MSQL。每项一个 feature commit。

## F00 规格基线

先测：文档链接、文件长度、重复术语和未登记文档检查应在当前树上通过。

开发：确定 `internal/` 包边界、测试目录、协议版本和 feature 编号；把当前设计作为实现基线。

提交：`docs(F00): freeze v0 implementation baseline`

## F01 Go 与 CLI 骨架

先测：`memora version --json`、`memora help`、未知命令退出码和 stdout/stderr golden。

开发：建立 Go module、`cmd/memora`、版本注入、退出码表；不接数据库。

提交：`feat(F01): bootstrap memora CLI`

## F02 测试基础设施

先测：示例测试能获得隔离 temp datadir、Fake Clock、Fake ID、fixture 和 fault point；泄漏检测能抓到写入真实用户目录。

开发：建立 unit/integration/e2e build tag、golden 更新工具、测试日志和统一 helper。

提交：`test(F02): add deterministic test harness`

## F03 CI 基线

先测：故意格式错误、失败测试、race fixture 和损坏 golden 会让本地 CI 脚本失败。

开发：加入 format、vet、unit、race、integration 分层命令和 GitHub Actions PR workflow；缓存不能改变结果。

提交：`ci(F03): enforce green Go test gates`

## F04 原型 Store 选型尖峰

先测：候选 SQLite 驱动必须通过事务回滚、并发读、进程重启、Unicode、macOS arm64/amd64 构建和无 CGO 发布验证。

开发：定义窄 `Store` contract，完成一次性适配尖峰并记录 ADR；业务包不得导入 SQLite driver。

提交：`feat(F04): establish replaceable prototype store`

## F05 macOS Instance 初始化

先测：默认 Application Support 路径、绝对路径覆盖、目录权限、重复 init、损坏 meta 和禁止把数据库放进 Caches。

开发：实现 `memora init`、最小 `instance.meta`、format version、instance UUID 和原子初始化。

提交：`feat(F05): initialize macOS instance`

## F06 配置与宿主边界

先测：配置优先级、非法值、宿主环境变量不进入 datadir/日志/导出，以及 Memora 不要求模型 API Key。

开发：实现非秘密配置加载；明确模型凭据属于 Codex/Claude 宿主，v0 不提供 secret provider。

提交：`feat(F06): isolate host and database config`

## F07 daemon 生命周期

先测：start/status/stop、重复启动、stale PID/socket、意外退出、自动重连和一个 Instance 只能有一个 writer daemon。

开发：实现 `memora daemon`、Instance lock、Unix socket、信号处理和健康状态。

提交：`feat(F07): run single-instance local daemon`

## F08 IPC 传输与会话核心

先测：并发客户端、请求取消、超时、超大 frame、断线、跨 request session 和客户端退出后的事务清理。

开发：实现带协议版本的 length-prefixed JSON IPC、可并发 CLI client 和连接级 session cleanup hook。

提交：`feat(F08): add versioned daemon IPC`

## F08b daemon socket 接入

先测：短路径和 Instance 隔离、live/stale socket、daemon ping、启动就绪与退出清理。

开发：将 IPC 接入 daemon 和 CLI，使用仅当前用户可访问的短 Unix socket 路径。

提交：`feat(F08b): connect daemon IPC socket`

## F09 统一响应 Envelope

先测：成功、错误、warning、truncated、batch 和未知字段兼容 golden；任何错误都包含稳定 code。

开发：实现协议类型、序列化、错误 registry 和 request ID。

提交：`feat(F09): define stable result envelope`

## F10 MSQL Lexer

先测：大小写、Unicode identifier、字符串/注释、参数、分号、非法字符和 fuzz corpus。

开发：实现带 source span 的 tokenizer；不得用正则切完整语句。

提交：`feat(F10): tokenize MSQL safely`

## F11 MSQL Parser 核心

先测：SHOW、DESCRIBE、CREATE、SELECT、INSERT、UPDATE、DELETE 的 AST golden，以及错误位置和 fuzz 不崩溃。

开发：实现 recursive-descent/Pratt Parser、版本化 AST 和参数占位符。

提交：`feat(F11): parse core MSQL statements`

## F12 多语句与事务语法

先测：statement list、BEGIN/COMMIT/ROLLBACK、空语句、事务跨 request、非法嵌套和失败项定位。

开发：实现 batch AST 与 session transaction state；此项只冻结语法和状态机，不执行数据。

提交：`feat(F12): parse batch transaction boundaries`

## Phase A 退出测试

在全新临时用户目录启动 daemon；CLI 完成 init、version、健康检查和一组 MSQL parse 请求；进程重启、并发和错误 golden 全部通过。
