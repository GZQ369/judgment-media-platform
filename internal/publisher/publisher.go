package publisher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"vertical-intel-video-agent/internal/domain"
)

var (
	ErrConfirmationRequired = errors.New("explicit user confirmation is required")
	ErrNotConfigured        = errors.New("publisher is not configured")
	ErrUnsupportedPlatform  = errors.New("unsupported publishing platform")
)

// Publisher 是单个平台的发布适配器。适配器只能使用平台官方允许的接入方式。
type Publisher interface {
	Platform() domain.Platform
	Capability(context.Context) domain.PlatformPublishCapability
	Publish(context.Context, domain.PublishProjectRequest) (domain.PublicationResult, error)
}

// Registry 通过平台注册发布适配器，新增平台不需要修改业务流程。
type Registry struct {
	publishers map[domain.Platform]Publisher
}

func NewRegistry(publishers ...Publisher) *Registry {
	r := &Registry{publishers: map[domain.Platform]Publisher{}}
	for _, p := range publishers {
		if p != nil {
			r.publishers[p.Platform()] = p
		}
	}
	return r
}

func NewDefaultRegistry() *Registry {
	return NewRegistry(
		NewDouyinPublisher(DouyinConfig{
			AccessToken: os.Getenv("DOUYIN_ACCESS_TOKEN"),
			OpenID:      os.Getenv("DOUYIN_OPEN_ID"),
			APIBaseURL:  os.Getenv("DOUYIN_API_BASE_URL"),
		}),
		NewXiaohongshuPublisher(os.Getenv("XHS_SHARE_APP_KEY")),
		NewWechatChannelsPublisher(),
	)
}

func (r *Registry) Capabilities(ctx context.Context) []domain.PlatformPublishCapability {
	capabilities := make([]domain.PlatformPublishCapability, 0, len(r.publishers))
	for _, p := range r.publishers {
		capabilities = append(capabilities, p.Capability(ctx))
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Platform < capabilities[j].Platform })
	return capabilities
}

func (r *Registry) Publish(ctx context.Context, platform domain.Platform, req domain.PublishProjectRequest) (domain.PublicationResult, error) {
	p, ok := r.publishers[platform]
	if !ok {
		return domain.PublicationResult{}, fmt.Errorf("%w: %s", ErrUnsupportedPlatform, platform)
	}
	if !req.Confirm {
		return domain.PublicationResult{}, ErrConfirmationRequired
	}
	req.Platform = platform
	return p.Publish(ctx, req)
}
