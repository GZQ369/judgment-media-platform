package publisher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

type XiaohongshuPublisher struct {
	appKey string
}

func NewXiaohongshuPublisher(appKey string) *XiaohongshuPublisher {
	return &XiaohongshuPublisher{appKey: strings.TrimSpace(appKey)}
}

func (*XiaohongshuPublisher) Platform() domain.Platform { return domain.PlatformXiaohongshu }

func (p *XiaohongshuPublisher) Capability(context.Context) domain.PlatformPublishCapability {
	return domain.PlatformPublishCapability{
		Platform:                 domain.PlatformXiaohongshu,
		Mode:                     domain.PublicationModeClientHandoff,
		Configured:               p.appKey != "",
		SupportsVideo:            true,
		RequiresUserConfirmation: true,
		Description:              "使用小红书 Share Open SDK 唤起官方客户端发布编辑器；用户需在客户端确认，服务端不会伪报已发布。",
	}
}

func (p *XiaohongshuPublisher) Publish(_ context.Context, req domain.PublishProjectRequest) (domain.PublicationResult, error) {
	if !req.Confirm {
		return domain.PublicationResult{}, ErrConfirmationRequired
	}
	if p.appKey == "" {
		return domain.PublicationResult{}, fmt.Errorf("%w: set XHS_SHARE_APP_KEY", ErrNotConfigured)
	}
	if req.VideoURL == "" && req.VideoPath == "" {
		return domain.PublicationResult{}, fmt.Errorf("xiaohongshu sharing requires video_url or a client-accessible video_path")
	}
	payload := map[string]any{
		"app_key":           p.appKey,
		"media_type":        "video",
		"video_url":         req.VideoURL,
		"video_path":        req.VideoPath,
		"cover_url":         req.CoverURL,
		"cover_path":        req.CoverPath,
		"suggested_title":   req.Title,
		"suggested_caption": req.Caption,
		"suggested_tags":    req.Tags,
	}
	return domain.PublicationResult{
		Platform:      domain.PlatformXiaohongshu,
		Mode:          domain.PublicationModeClientHandoff,
		Status:        domain.PublicationStatusActionRequired,
		ActionURL:     "https://agora.xiaohongshu.com/",
		Message:       "已生成小红书官方分享 SDK 交接数据；请由已注册 App 唤起小红书并由用户完成发布确认。标题和正文自动填充受平台限制。",
		ClientPayload: payload,
		SubmittedAt:   time.Now().UTC(),
	}, nil
}
