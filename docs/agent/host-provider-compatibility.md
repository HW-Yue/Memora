# 宿主模型与 CC Switch 兼容边界

状态：F72 已确认并进入 Canonical Skill contract。

Memora v0 不内置模型 Provider，也不接收或保存模型密钥。自然语言推理发生在
Codex、Claude Code 等宿主中；Memora 只接收宿主生成的 MSQL、授权对象和逻辑
Mutation Plan。

## 兼容规则

- OpenAI-compatible 只表示协议兼容，不表示 `openai.com`；base URL、模型名和
  API key 均由宿主配置。
- Kimi 等由 CC Switch 暴露的 OpenAI-compatible 地址可以作为宿主 Provider。
- Claude Code 可使用 CC Switch 提供的 Anthropic-compatible 配置。
- CC Switch 是可选宿主配置，不是 Memora 运行依赖，也不进入数据库协议。
- Provider 地址、API key、Bearer token 不得出现在 `memora` 命令参数、Config、
  Row、History、Route、日志、收据、快照或 Wiki 导出中。
- Provider 离线或切换时，已经生成的 MSQL 仍由同一个确定性引擎执行；数据库
  状态不依赖任何模型官网或供应商。

Canonical contract 用 `provider_boundary` 固定上述边界：owner 为 `host`，endpoint
由 host 配置，凭据永不传给 Memora，并声明 OpenAI-compatible 与
Anthropic-compatible 两种宿主协议。

## 验收

Codex 与 Claude Code 适配包必须来自同一 Canonical Skill，协议 digest 相同；
Memora Config 继续拒绝 `api_key` 等秘密字段。测试不得打印或复制 CC Switch 中
的真实凭据。
