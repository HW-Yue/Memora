# 进程配置与宿主边界

状态：F06 已实现。

## 范围

本配置只处理启动本地进程所需的非秘密值：

- `instance`：本地 Instance 名称；
- `data_dir`：显式绝对 datadir；
- `log_level`：`debug/info/warn/error`。

字段长度、检索权重、Router 预算等数据库逻辑配置不属于本文件，必须保存在 Data Dictionary 中并通过 MSQL 修改。

## 来源与优先级

从低到高：

```text
内置默认值
< ~/Library/Application Support/Memora/config.json
< MEMORA_INSTANCE / MEMORA_DATA_DIR / MEMORA_LOG_LEVEL
< CLI --instance / --data-dir / --log-level
```

`--config /absolute/path` 可以显式选择另一份 JSON。显式路径不存在或配置无效时直接报错；默认配置文件不存在属于正常首次运行。

JSON 使用严格字段检查。拼错字段或加入 `api_key`、`model_api_key` 等未定义字段都会失败，不能静默忽略。

## 模型宿主边界

Skill-first v0 的模型由 Codex/Claude Code 提供。Memora：

- 不读取 `OPENAI_API_KEY`、`ANTHROPIC_API_KEY` 或 `MEMORA_API_KEY`；
- 不在 Config 结构中提供任意 secret 字段；
- 不把宿主环境整体复制进 datadir、日志、导出或诊断；
- 不要求用户为本地 daemon 重复配置模型凭据。

未来若增加内置 Provider，必须另立安全规格并使用系统凭据存储，不能扩展当前 JSON 来保存明文密钥。

F51 的受控离线 benchmark 是工具边界内的例外：`run-ai-native-benchmark` 可从
`MEMORA_BENCHMARK_OPENAI_*` 进程环境临时读取兼容地址、key 和模型。它不启动
daemon Provider、不写 Config/datadir，报告只保留 endpoint host、模型、token
计数和 hash，不保存地址中的凭据、请求 header 或 key。

## 校验

- Instance 名只允许 Unicode 字母/数字及 `-_.`，不能是空、`.` 或 `..`；
- datadir 非空时必须是绝对路径；
- 日志级别大小写归一化后必须属于允许集合；
- 配置序列化只包含上述公开非秘密字段。
