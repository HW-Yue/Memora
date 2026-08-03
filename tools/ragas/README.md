# Memora Ragas evaluator

该目录是开发/测评工具，不进入 Memora 二进制或安装包。依赖固定为 Ragas v0.4.3，使用
collections API 的 `FactualCorrectness`、`Faithfulness`、`ContextPrecision` 与
`ContextRecall`。Python 3.9 额外锁定 `eval-type-backport`，用于兼容 Ragas 当前解析到的
Instructor 类型注解。

建议在独立虚拟环境安装：

```sh
python3 -m venv /private/tmp/memora-ragas-venv
/private/tmp/memora-ragas-venv/bin/python -m pip install -r tools/ragas/requirements.txt
```

随后由 Go 命令启动 Python；只把配置中明确指定的 key 交给 evaluator：

```sh
go run ./cmd/run-answer-evaluation \
  --manifest /absolute/manifest.json \
  --ground-truth /absolute/ground-truth.json \
  --scorecard /absolute/scorecard.json \
  --diagnostics /absolute/diagnostics.json \
  --evaluator /private/tmp/memora-ragas-venv/bin/python \
  --evaluator-arg /absolute/tools/ragas/evaluate.py \
  --api-base-url https://api.moonshot.cn/v1 \
  --judge-model moonshot-v1-8k \
  --secret-env MOONSHOT_API_KEY \
  --output /absolute/evaluation.json
```

Python 只返回逐题分数或稳定错误码；不返回 reference、response、SQL evidence、异常文本或 key。
输出先由 Go 信任边界规范化并签名，再进入公开聚合报告。Adapter 会强制关闭 Ragas usage
analytics，避免私有评测元数据离开显式配置的 judge 链路。
