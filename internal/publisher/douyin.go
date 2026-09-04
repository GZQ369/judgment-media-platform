package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vertical-intel-video-agent/internal/domain"
)

type DouyinConfig struct {
	AccessToken string
	OpenID      string
	APIBaseURL  string
	HTTPClient  *http.Client
}

type DouyinPublisher struct {
	config DouyinConfig
}

func NewDouyinPublisher(config DouyinConfig) *DouyinPublisher {
	if strings.TrimSpace(config.APIBaseURL) == "" {
		config.APIBaseURL = "https://open.douyin.com"
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &DouyinPublisher{config: config}
}

func (*DouyinPublisher) Platform() domain.Platform { return domain.PlatformDouyin }

func (p *DouyinPublisher) Capability(context.Context) domain.PlatformPublishCapability {
	return domain.PlatformPublishCapability{
		Platform:                 domain.PlatformDouyin,
		Mode:                     domain.PublicationModeDirectAPI,
		Configured:               p.config.AccessToken != "" && p.config.OpenID != "",
		SupportsVideo:            true,
		RequiresUserConfirmation: true,
		Description:              "使用抖音开放平台 video.create 权限上传视频并创建投稿；返回 submitted 表示已提交平台处理。",
	}
}

func (p *DouyinPublisher) Publish(ctx context.Context, req domain.PublishProjectRequest) (domain.PublicationResult, error) {
	if !req.Confirm {
		return domain.PublicationResult{}, ErrConfirmationRequired
	}
	if p.config.AccessToken == "" || p.config.OpenID == "" {
		return domain.PublicationResult{}, fmt.Errorf("%w: set DOUYIN_ACCESS_TOKEN and DOUYIN_OPEN_ID", ErrNotConfigured)
	}
	if strings.TrimSpace(req.VideoPath) == "" {
		return domain.PublicationResult{}, fmt.Errorf("douyin direct publishing requires video_path")
	}
	videoID, err := p.uploadVideo(ctx, req.VideoPath)
	if err != nil {
		return domain.PublicationResult{}, err
	}
	itemID, err := p.createVideo(ctx, videoID, composeText(req))
	if err != nil {
		return domain.PublicationResult{}, err
	}
	return domain.PublicationResult{
		Platform:    domain.PlatformDouyin,
		Mode:        domain.PublicationModeDirectAPI,
		Status:      domain.PublicationStatusSubmitted,
		ExternalID:  itemID,
		Message:     "视频已通过抖音开放平台提交，最终可见状态以平台审核结果为准。",
		SubmittedAt: time.Now().UTC(),
	}, nil
}

func (p *DouyinPublisher) uploadVideo(ctx context.Context, videoPath string) (string, error) {
	file, err := os.Open(filepath.Clean(videoPath))
	if err != nil {
		return "", fmt.Errorf("open douyin video: %w", err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("video", filepath.Base(videoPath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	var response douyinResponse
	if err := p.do(ctx, http.MethodPost, "/video/upload/", writer.FormDataContentType(), &body, &response); err != nil {
		return "", err
	}
	if response.Data.Video.VideoID == "" {
		return "", fmt.Errorf("douyin upload returned no video_id")
	}
	return response.Data.Video.VideoID, nil
}

func (p *DouyinPublisher) createVideo(ctx context.Context, videoID, text string) (string, error) {
	payload, err := json.Marshal(map[string]string{"video_id": videoID, "text": text})
	if err != nil {
		return "", err
	}
	var response douyinResponse
	if err := p.do(ctx, http.MethodPost, "/video/create/", "application/json", bytes.NewReader(payload), &response); err != nil {
		return "", err
	}
	if response.Data.ItemID == "" {
		return "", fmt.Errorf("douyin create returned no item_id")
	}
	return response.Data.ItemID, nil
}

func (p *DouyinPublisher) do(ctx context.Context, method, path, contentType string, body io.Reader, target *douyinResponse) error {
	endpoint, err := url.Parse(strings.TrimRight(p.config.APIBaseURL, "/") + path)
	if err != nil {
		return err
	}
	query := endpoint.Query()
	query.Set("open_id", p.config.OpenID)
	endpoint.RawQuery = query.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return err
	}
	httpReq.Header.Set("access-token", p.config.AccessToken)
	httpReq.Header.Set("Content-Type", contentType)
	resp, err := p.config.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("douyin request: %w", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode douyin response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("douyin http status %d: %s", resp.StatusCode, target.description())
	}
	if target.Data.ErrorCode != 0 {
		return fmt.Errorf("douyin error %d: %s", target.Data.ErrorCode, target.description())
	}
	return nil
}

type douyinResponse struct {
	Data struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		ItemID      string `json:"item_id"`
		Video       struct {
			VideoID string `json:"video_id"`
		} `json:"video"`
	} `json:"data"`
	Extra struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	} `json:"extra"`
}

func (r douyinResponse) description() string {
	if r.Data.Description != "" {
		return r.Data.Description
	}
	return r.Extra.Description
}

func composeText(req domain.PublishProjectRequest) string {
	parts := []string{strings.TrimSpace(req.Title), strings.TrimSpace(req.Caption)}
	for _, tag := range req.Tags {
		tag = strings.TrimSpace(strings.TrimPrefix(tag, "#"))
		if tag != "" {
			parts = append(parts, "#"+tag)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
