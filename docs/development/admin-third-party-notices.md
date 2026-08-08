# Admin 第三方前端资源

Admin 仍然以单个离线 `memora` binary 发布。以下资源在构建时固定版本、逐字节校验并
通过 `go:embed` 内嵌，运行时不从 CDN 或网络加载：

| Resource | Version | License | 用途 |
| --- | --- | --- | --- |
| [AntV G6](https://github.com/antvis/G6) | 5.1.1 | MIT | 语义索引树画布、布局、拖拽、缩放和折叠 |
| [markdown-it](https://github.com/markdown-it/markdown-it) | 15.0.0 | MIT | Markdown 文档渲染 |
| [DOMPurify](https://github.com/cure53/DOMPurify) | 3.4.7 | Apache-2.0 / MPL-2.0 | 渲染结果的浏览器 DOM 清理 |

对应的压缩资源位于 `internal/adminui/dist/assets/vendor/`；资源哈希和大小冻结在
`internal/adminui/bundle.go`。升级任一资源必须重新生成校验值、运行 Admin bundle
测试，并在发布说明中记录版本和许可证变化。
