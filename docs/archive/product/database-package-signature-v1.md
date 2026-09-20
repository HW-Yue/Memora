# Database Package Signature v1

状态：F137 已实现。

## 格式与签名对象

`memora.database-package/v1` 保持兼容，在 envelope 顶层可选携带：

```json
{"signature":{"algorithm":"Ed25519","key_id":"publisher:key","public_key":"base64","subject_sha256":"hex","value":"base64"}}
```

签名 payload 是省略 `signature` 后的完整确定性 envelope JSON 字节，因而同时绑定 manifest、
snapshot、对象计数和 snapshot hash。`subject_sha256` 是该 payload 的 SHA-256；签名算法只接受
Ed25519。固定 logical snapshot、作者、key ID 与私钥产生完全相同的包字节。

## API 与边界

`PackSigned` 只接收调用方短期持有的私钥，包中只写公钥；Memora 不保存或生成私钥。
`Open` 总是先验证 authority 和 snapshot hash，再验证可选签名，并只在成功后返回
`Signature.Verified=true`。任何编码、key 长度、subject 或签名差异都稳定拒绝。

未签名包仍可只读打开，以保留 F44 格式兼容；已验证签名是否受本地信任、能否安装由 F138
策略决定，撤销由 F140 决定。自带公钥只证明包未被签名后修改，不单独证明发布者身份。
