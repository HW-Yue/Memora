# 安全与隐私门 v1

状态：F46 实现规格；边界已冻结。

## 威胁模型

Memora 保护不同逻辑 Database 的授权边界，避免 Agent 因参数、路径、恶意文本或高风险命令误操作。v0 不把同一 macOS 用户下可任意读写 Memora 文件的本地进程视为隔离租户；操作系统用户仍是最终信任边界。

宿主 Agent 必须在每个结构化 MSQL input 中携带 `memora.authorization/v1`，包含 actor 和本次允许访问的 Database 名称或稳定 ID。直接使用内部 Go API、或由本地用户运行不带 Authorization 的普通 SQL，属于可信本地操作员路径；数据打包、Wiki 导出和安装等管理操作不使用这个降级。

## Policy enforcement

- Authorization 最多列出 32 个非空、去重的 Database selector；仅安装 approval 可以没有既有 Database scope；
- 带 Authorization 的语句只能引用列表内的 Database；
- `PACK DATABASE` 与 `EXPORT WIKI` 必须带非空 Database scope；
- Wiki 只投影 scope 内的 Row；跨 scope 关系不生成链接；
- Wiki manifest 的 snapshot hash 只覆盖授权投影，不反映 scope 外变化；
- `INSTALL PACKAGE` 除 `TRUSTED` 语法外，还需要绑定动作和 package SHA-256 的显式 approval；
- approval 只是结构化的用户确认收据，不是对同一 OS 用户的密码学认证；
- Skill Mutation/Schema Plan 把自己的 `authorized_databases` 传播到每次 MSQL 调用，不能只在宿主侧检查。

## 外部路径

写出 package 或 Wiki 时，目标必须是绝对、规范化路径。导出器逐级拒绝目标目录中的符号链接和非目录父节点，manifest 也不能是符号链接；稳定 ID 生成的相对路径再次经过 containment 校验。

F46 只允许写调用方显式选择的外部目录。它不授予访问任意 Database 的权限，路径授权与 Database scope 必须同时满足。

## 不可信 package 文本

Package 仍是严格、版本化 JSON，不携带代码。解码设置总大小预算；manifest 的名称、purpose、scope、anti-scope 和 author 必须是有效 UTF-8、单行、有界文本，拒绝控制字符。Row 正文可包含任意合法业务文本，但只作为数据导入，不能成为命令、Policy 或 approval。

## 审计与脱敏

daemon 为每个 IPC request 追加审计事件，只保存事件 ID、时间、request ID、method、actor、授权 scope、payload SHA-256、结果状态和稳定错误码。不得保存 MSQL 原文、参数、Row、package、Profile、模型密钥或原始 payload。

面向 stderr 的兜底错误输出会屏蔽常见 API key、Bearer token 和 `*_KEY=...` 形式；稳定业务错误应从源头避免包含秘密。

## Doctor

`memora doctor` 除逻辑 snapshot 外还验证：

- Policy/Authorization 版本；
- 外部模型 Provider 固定为 disabled/host-only；
- audit 记录可以严格解码、版本正确且哈希字段有效；
- 安全检查失败时不能返回 `healthy`。

## 非目标

- 防御已取得同一 macOS 用户文件权限的恶意程序；
- 内置模型 Provider 或密钥存储；
- package 发布者签名与远端身份；
- F47/F48 的制品签名、Gatekeeper 和 Release 自动化。
