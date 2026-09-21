# 多平台发布模块

代码位置：`internal/publisher`。

## 目标

历史 project publishing API 曾允许一次确认后请求抖音、小红书和视频号；该兼容路径正在禁用，不能作为新的真实发布入口。当前正式入口是 `internal/publication` 的 Publication Orchestrator，allowlist 为抖音、小红书和 Bilibili，视频号不在该事务入口中。系统对外统一返回平台结果，但不会掩盖不同平台官方开放能力的差异。

状态含义：

- `submitted`：已通过官方 API 提交，仍可能处于平台审核中。
- `action_required`：发布资料已经准备好，用户还要在官方 App 或创作者后台完成确认。
- `failed`：未配置、参数错误、平台 API 错误或网络错误。

## 平台实现

### 抖音

- 模式：`direct_api`。
- 使用官方 `POST /video/upload/` 上传，再使用 `POST /video/create/` 创建投稿。
- 需要创作者 OAuth 授权和 `video.create` 权限。
- 环境变量：`DOUYIN_ACCESS_TOKEN`、`DOUYIN_OPEN_ID`。
- 可选测试变量：`DOUYIN_API_BASE_URL`。
- 成功返回 `submitted`，不直接声明内容已经公开可见。

官方文档：

- https://open.douyin.com/platform/resource/docs/openapi/video-management/douyin/create/upload/
- https://open.douyin.com/platform/resource/docs/openapi/video-management/douyin/create/create-video
- https://open.douyin.com/platform/resource/docs/accession-guide/type-and-permission

当前仍需补齐生产级 OAuth 登录、token 刷新、平台审核状态查询和账号解绑。

### 小红书

- 模式：`client_handoff`。
- 旧 `internal/publisher` contract 生成可由已注册客户端消费的小红书 Share Open SDK 交接数据；服务端不直接唤起编辑器或完成投稿。
- 环境变量：`XHS_SHARE_APP_KEY`，只用于该客户端交接，不是服务端一键发布凭据。
- API 返回 `client_payload`，由已注册的 iOS、Android、HarmonyOS 或 JavaScript 客户端消费。
- 用户必须在小红书客户端完成最终确认；标题和正文自动填充受平台规则限制。
- Publication Orchestrator 的一次性授权 commit 不选用此官方交接 adapter；当前小红书提交路径是 Ego，未配置 Ego 时返回能力阻塞。

官方文档：

- https://agora.xiaohongshu.com/
- https://agora.xiaohongshu.com/doc
- https://agora.xiaohongshu.com/doc/qa

小红书账号开放平台当前公开的账号授权能力不等同于服务端笔记投稿 API，因此本项目不会把 SDK 交接伪报成“发布成功”。

### 视频号

- 模式：`assisted`。
- 返回视频号助手的官方操作入口和待发布字段。
- 用户在视频号助手选择视频并完成最终确认。
- 不使用 Cookie、逆向接口或其他未公开服务端投稿方式。

当前未发现面向普通创作者的公开服务端视频投稿 API，因此视频号不能在合规前提下实现与抖音完全相同的静默服务端代发。

## Legacy API（兼容、逐步禁用）

以下接口只描述旧 `internal/publisher` contract，不代表已通过新的发布安全门禁：

查看平台能力：

```http
GET /api/v1/publishing/platforms
```

一次请求三个平台：

```http
POST /api/v1/projects/{project_id}/publish
Content-Type: application/json

{
  "platforms": ["douyin", "xiaohongshu", "wechat_channels"],
  "video_path": "data/videos/final.mp4",
  "video_url": "https://cdn.example.com/final.mp4",
  "cover_url": "https://cdn.example.com/cover.jpg",
  "confirm": true
}
```

`confirm=true` 只是旧接口的兼容字段。新的发布必须通过 bearer token、候选校验、独立授权确认、服务端 commit `confirm=true`、精确 fingerprint 和平台证据校验；不能直接调用旧接口绕过 Publication Orchestrator。标题、正文和标签不填写时会使用项目 brief；也可通过 `overrides` 为每个平台覆盖。

## 扩展平台

新增平台只需实现：

```go
type Publisher interface {
    Platform() domain.Platform
    Capability(context.Context) domain.PlatformPublishCapability
    Publish(context.Context, domain.PublishProjectRequest) (domain.PublicationResult, error)
}
```

然后将适配器注册到 `publisher.Registry`。平台密钥留在服务端配置中，不放进发布请求或项目导出文件。
