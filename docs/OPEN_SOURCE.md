# 开源使用说明

本仓库以 Apache License 2.0 发布，版权声明为 Copyright 2026 GZQ369。许可证正文见仓库根目录 [LICENSE](../LICENSE)。

## 本地运行

后端要求 Go 1.23 或更新版本。默认运行路径不需要模型凭据：

```bash
go test ./...
go run ./cmd/server
```

API 默认监听 `127.0.0.1:8088`，健康检查为 `GET /health`。本地运行数据写入 `data/`。容器启动方式为 `docker compose up --build`；Docker 镜像是 API-only，不包含 Vue 看板或媒体生产环境。

Research/Judgment parent pipeline 不是无来源的通用问答接口。请求必须提供至少一个有效 HTTPS 来源 URL，并且服务必须配置 OpenAI-compatible gateway：`AI_GATEWAY_BASE_URL` 与 `AI_GATEWAY_MODEL` 成对设置，按 gateway 要求提供 `AI_GATEWAY_API_KEY`。缺少 gateway 时 API 和确定性 workflow 仍可使用，但 Research pipeline 会以能力不可用状态结束。

可选前端位于 `web/`，使用 Vue 3、Vite 和 npm，在本机安装 Node.js/npm 后可通过 `npm install`、`npm run dev` 启动。它不是 Docker 镜像的一部分。

## 集成边界

- tRPC-Agent-Go 已由 `internal/mediaworker` 用于有界的 OpenMontage 渲染 GraphAgent/Runner 执行；主内容生产 workflow 仍以确定性步骤为主，不是自主规划或自主角色动画系统。
- Publication Orchestrator 当前入口面向抖音、小红书和 Bilibili；视频号尚未接入。适配器存在不代表真实账号投稿已在所有环境验证。
- Docker 只打包 Go API。渲染需要使用者自行准备并配置 OpenMontage、Piper 语音模型、FFmpeg/ffprobe、Remotion/Node.js、Python 及所需素材；这些运行时、模型和素材不由本仓库提供。

## 第三方组件、模型与素材

仓库中的集成代码可能与 OpenMontage、Piper、FFmpeg、Remotion 及其他外部组件协作，但这些项目/运行时、预训练模型、语音模型、媒体素材和账号凭据不随本仓库分发。使用者应自行取得所需组件、检查各自适用的许可证和使用条款，并遵守其要求。本说明不对任何第三方项目的具体许可证、模型权利、素材权利或特定用途作出确认或法律结论。

运行时生成的数据、用户提供的来源内容、模型输出、导出媒体及发布凭据也不属于本仓库随附内容。请勿将私有凭据、本地 `data/`、账号登录态或未获授权的素材提交到公开仓库。
