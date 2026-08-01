# F150 Public Release Publish 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

只有 verified signed tag 对应的、完整通过双架构 smoke/clean-machine/trusted signature 门的固定
publication，才可由唯一 write job 以 draft staging 后转成正式 GitHub Release。

## RED

- publication build 强制要求 F149 signer；缺 key/key ID 不生成目录。
- verify-publication 的 publish path 强制显式 key ID + 32-byte public key trust root。
- GitHub encrypted secret 只落到 runner 私有临时文件，不进入 artifact、日志或 repo。
- 固定上传集合新增 `release.sig`；draft validator 拒绝缺失、额外、空和重复资产。
- publish 前再次 VerifySigned；draft 创建/上传/核对任一步失败只删除本次 draft，不动 tag/旧 Release。
- publish job 是唯一 `contents:write`；其他 build/smoke/acceptance/gate job 保持 read-only。

## 边界

F150 实现发布能力但不在开发/测试中创建真实 Release。真正发布仍要求仓库管理员配置 secret、
创建并推送 verified annotated stable tag。

## 完成门

missing signer、wrong trust、asset set、workflow static gate、双架构 smoke/acceptance、race 与全仓 CI
全绿后合入。下一项 F151 evidence gate。

## 完成证据

- publication build 缺 signer 在写入输出目录前失败，错误 trust root 无法验证签名；
- build、smoke、release gate 与 publish 均从独立 public-key secret 执行 `VerifySigned`；
- workflow 私钥只进入 runner 私有临时文件，发布固定集合为含 `release.sig` 的七项资产；
- draft 失败清理仍只作用于本次 draft，publish 仍是唯一 write job；
- targeted、race 与全仓 CI 全绿。下一项 F151。
