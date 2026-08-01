# F137 Package Signature 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

Database Package 可以携带可离线验证的 Ed25519 发布者签名；签名精确绑定无签名 envelope
的 SHA-256、manifest 与逻辑 snapshot，重打包仍保持确定性。

## RED

- 固定私钥签出的相同逻辑包字节完全一致，Open 返回 verified signer。
- snapshot、manifest、subject、公钥或签名字节任一变化都拒绝。
- 非 Ed25519、非法 key ID、错误 key 长度和未知字段拒绝。
- 未签名 v1 包仍可只读 Open；是否允许安装由 F138 决定。

## 非目标

不在包中保存私钥，不定义远端 CA、商业授权、撤销和安装信任策略；撤销属于 F140。

## 完成证据

Ed25519 确定性、tamper、未签名兼容测试与 race 通过；全仓 CI 全绿。下一项 F138。
