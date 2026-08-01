# F149 Release Artifacts 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

darwin/arm64 与 darwin/amd64 的可复现 release 集合可附带确定性 Ed25519 detached signature，
验证方可用显式 trust root 同时验证 manifest、checksums 与两份 archive。

## RED

- `release.sig` 绑定 canonical `release.json` 与 `checksums.txt` SHA-256，使用 domain-separated payload。
- signer key ID、Ed25519 public key、算法、两项 hash 和 signature 严格解析，无未知字段。
- Build 在 staging 内签名并整体 Verify 后发布；错误 key/signature/fault 不发布输出目录。
- 同一 source/toolchain/epoch/key 逐字节重建一致；两架构 archive 仍保持原 reproducibility。
- VerifySigned 要求调用方显式提供匹配 key ID/public key，不能把包内自报 key 当 trust root。
- unsigned F47 制品仍可离线 Verify 以兼容历史；F149 publish path 必须调用 VerifySigned。
- native smoke 继续执行 version/init/daemon/doctor，并在执行前完成 signed set 验证。

## 边界

F149 是供应链 detached signature，不冒充 Apple Developer ID/notarization。后者需要外部证书、
Apple 服务与不可复现 timestamp，若加入必须作为独立签名层记录。

## 完成门

determinism、trusted/wrong key、tamper、strict layout、双架构 smoke、race 与全仓 CI 全绿后合入。
下一项 F150。

## 完成证据

双架构 archive 原有 determinism/smoke 保持；signed set 重建逐字节一致，trusted/wrong key、
signature tamper、strict file set 与私钥权限入口均有门禁；race 与全仓 CI 全绿。下一项 F150。
