package publisher

import (
	"context"
	"fmt"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

type WechatChannelsPublisher struct{}

func NewWechatChannelsPublisher() *WechatChannelsPublisher { return &WechatChannelsPublisher{} }

func (*WechatChannelsPublisher) Platform() domain.Platform { return domain.PlatformWechatChannels }

func (*WechatChannelsPublisher) Capability(context.Context) domain.PlatformPublishCapability {
	return domain.PlatformPublishCapability{
		Platform:                 domain.PlatformWechatChannels,
		Mode:                     domain.PublicationModeAssisted,
		Configured:               true,
		SupportsVideo:            true,
		RequiresUserConfirmation: true,
		Description:              "普通创作者暂无公开的服务端投稿 API；使用视频号助手完成用户确认发布，不调用 Cookie 或逆向私有接口。",
	}
}

func (*WechatChannelsPublisher) Publish(_ context.Context, req domain.PublishProjectRequest) (domain.PublicationResult, error) {
	if !req.Confirm {
		return domain.PublicationResult{}, ErrConfirmationRequired
	}
	if req.VideoPath == "" && req.VideoURL == "" {
		return domain.PublicationResult{}, fmt.Errorf("wechat channels assisted publishing requires video_path or video_url")
	}
	return domain.PublicationResult{
		Platform:  domain.PlatformWechatChannels,
		Mode:      domain.PublicationModeAssisted,
		Status:    domain.PublicationStatusActionRequired,
		ActionURL: "https://channels.weixin.qq.com/platform/post/create",
		Message:   "发布包已准备好；请在官方视频号助手中选择视频并完成最终发布确认。当前未使用非公开代发接口。",
		ClientPayload: map[string]any{
			"video_path": req.VideoPath,
			"video_url":  req.VideoURL,
			"cover_path": req.CoverPath,
			"cover_url":  req.CoverURL,
			"title":      req.Title,
			"caption":    req.Caption,
			"tags":       req.Tags,
		},
		SubmittedAt: time.Now().UTC(),
	}, nil
}
