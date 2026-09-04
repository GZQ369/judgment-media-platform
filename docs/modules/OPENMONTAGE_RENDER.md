# OpenMontage Render 模块

## 模块职责

`internal/render` 保持现有 OpenMontage 集成，作为 batch 的可选增强路径。MVP 主价值是发布包，成片渲染不应阻断发布包生成。

## 关键文件

- `internal/render/openmontage.go`
- `internal/engine/engine.go`

## 输入 / 输出

输入是 `domain.VideoProject` 和 `domain.RenderOpenMontageRequest`。输出是 `domain.RenderResult`，包括 props、request、log、video、字幕和 provider capability。

## 调用链路

单项目接口调用 `POST /api/v1/projects/{id}/render/openmontage`。Batch 中只有 `render=true` 才调用 `Engine.RenderOpenMontage`。

## 与 tRPC-Agent-Go 的关系

OpenMontage 可视为 RenderPlannerAgent 和渲染 Tool 的后端实现。渲染 props、日志和 mp4 都是 Artifact。

## 无 API key fallback

无 API key 时仍可生成 props、request 和 provider notes。若本机缺少 OpenMontage/Remotion/FFmpeg，render 会失败，但 batch item 会保留发布包路径并记录 error。

## 测试方法

主路径测试使用 render=false：

```bash
go test ./internal/engine
```

本机具备 OpenMontage 依赖时，可手动用 render=true 请求验证。

## 常见失败与排查

- 找不到 OpenMontage：确认 `D:/agent/video-workers/OpenMontage` 存在。
- 找不到 remotion composer：确认 `remotion-composer/src/index.tsx` 存在。
- 输出 mp4 缺失：查看 `openmontage_render.log`。
