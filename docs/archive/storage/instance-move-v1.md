# Instance Move v1

状态：F145 已实现。

## 用户入口

```text
memora move --data-dir /absolute/source \
  --backup /absolute/portable-backup \
  --target /absolute/new-device/instance
```

`--data-dir` 可省略并使用当前配置。backup 与 target 必须不存在；三个路径必须绝对、
规范化且互不包含。成功输出 `memora.instance-move-receipt/v1` JSON。

## 状态机

1. 检查 source daemon 状态；运行中则停止。
2. 用 F143 创建并验证 portable backup。
3. 用 F144 在 target 的同一文件系统 staging，完整重验后原子发布。
4. source 原本运行时启动 target；原本停止时保持 target 停止。
5. stop 后任一步失败，尽力重新启动 source，并把恢复失败与原错误一起返回。

## 安全边界

- source 永不自动删除，收据固定 `source_retained: true`；因此目标启动失败仍可回退。
- 不覆盖 backup 或 target，不修改配置，不跨目录直接 rename 数据文件。
- backup 是独立、可校验的搬迁证据，不因 move 成功而删除。
- launchd 注册和默认目录切换不属于本协议；分别由 F148 和用户配置负责。

相关原语：[Instance Portable Backup v1](./instance-portable-backup-v1.md)、
[Instance Restore v1](./instance-restore-v1.md)。
