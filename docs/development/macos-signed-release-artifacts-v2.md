# macOS Signed Release Artifacts v2

状态：F149 已实现；替代 F47 对“正式制品无签名”的结论。

## 文件集合

两份可复现 archive、`release.json`、`checksums.txt` 保持 F47 格式，新增严格 JSON
`release.sig`。签名对象版本为 `memora.release-signature/v1`，包含：

- `algorithm: Ed25519`、稳定 key ID 与 base64 public key；
- `release.json` 和 `checksums.txt` 的 SHA-256；
- 对 domain-separated canonical metadata 的 Ed25519 signature。

签名不进入自己的 checksum，因此没有循环依赖。相同 source、Go toolchain、epoch 与 key 会生成
逐字节相同的五个 release 文件。

## 构建与验证

私钥文件是单行 base64 32-byte seed 或 64-byte private key，必须是 regular、非 symlink 且不向
group/other 开放。正式构建要求：

```text
MEMORA_RELEASE_SIGNING_KEY_FILE=/private/key \
MEMORA_RELEASE_SIGNER_KEY_ID=memora:release:2026 \
scripts/release.sh 0.1.0 /absolute/new-output
```

`release.Verify` 验证自包含完整性并兼容历史 unsigned F47 集合；正式消费必须用
`release.VerifySigned` 或 `cmd/verify-release` 传入显式 key ID/public key trust root。包内自报 public
key 不能自行建立信任。

## Apple 签名边界

Ed25519 证明 Memora 发布者与文件集合，不等于 Apple Developer ID、hardened runtime 或
notarization ticket。Apple 层依赖外部证书和服务，其 timestamp 也不属于可复现 payload；若后续
加入，应作为 archive 之前的额外签名层并单独记录证据。

历史格式见 [macOS Release 制品 v1](../archive/design/macos-release-artifacts-v1.md)。
