// Package exporter 负责写出视频工厂的本地发布包和 batch report。
//
// Engine 只负责应用编排，具体 Markdown/JSON 文件由本包统一生成。
// 这样后续无论接 tRPC-Agent-Go Artifact、压缩包、对象存储还是 Web 预览，
// 都可以从一个清晰的导出边界扩展。
package exporter
