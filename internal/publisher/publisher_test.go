package publisher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

func TestDouyinPublisherUploadsThenCreatesVideo(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("access-token") != "token" || r.URL.Query().Get("open_id") != "creator" {
			t.Fatalf("missing douyin credentials: headers=%v query=%s", r.Header, r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/video/upload/":
			calls = append(calls, "upload")
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse upload: %v", err)
			}
			file, _, err := r.FormFile("video")
			if err != nil {
				t.Fatalf("video field missing: %v", err)
			}
			_ = file.Close()
			_, _ = w.Write([]byte(`{"data":{"error_code":0,"video":{"video_id":"video-1"}}}`))
		case "/video/create/":
			calls = append(calls, "create")
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			if body["video_id"] != "video-1" || body["text"] == "" {
				t.Fatalf("unexpected create body: %+v", body)
			}
			_, _ = w.Write([]byte(`{"data":{"error_code":0,"item_id":"item-1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	videoPath := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewDouyinPublisher(DouyinConfig{AccessToken: "token", OpenID: "creator", APIBaseURL: server.URL, HTTPClient: server.Client()})
	result, err := p.Publish(context.Background(), domain.PublishProjectRequest{
		VideoPath: videoPath,
		Title:     "标题",
		Caption:   "正文",
		Tags:      []string{"测试"},
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"upload", "create"}) {
		t.Fatalf("unexpected calls: %v", calls)
	}
	if result.Status != domain.PublicationStatusSubmitted || result.ExternalID != "item-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestOfficialHandoffPublishersRequireFinalUserAction(t *testing.T) {
	xhs := NewXiaohongshuPublisher("app-key")
	xhsResult, err := xhs.Publish(context.Background(), domain.PublishProjectRequest{VideoURL: "https://example.com/video.mp4", Confirm: true})
	if err != nil {
		t.Fatalf("xiaohongshu publish: %v", err)
	}
	if xhsResult.Status != domain.PublicationStatusActionRequired || xhsResult.Mode != domain.PublicationModeClientHandoff {
		t.Fatalf("xiaohongshu must require client action: %+v", xhsResult)
	}

	channels := NewWechatChannelsPublisher()
	channelsResult, err := channels.Publish(context.Background(), domain.PublishProjectRequest{VideoPath: "video.mp4", Confirm: true})
	if err != nil {
		t.Fatalf("wechat channels publish: %v", err)
	}
	if channelsResult.Status != domain.PublicationStatusActionRequired || channelsResult.Mode != domain.PublicationModeAssisted {
		t.Fatalf("wechat channels must require user action: %+v", channelsResult)
	}
}

func TestRegistryRequiresExplicitConfirmation(t *testing.T) {
	registry := NewRegistry(NewWechatChannelsPublisher())
	_, err := registry.Publish(context.Background(), domain.PlatformWechatChannels, domain.PublishProjectRequest{VideoPath: "video.mp4"})
	if err != ErrConfirmationRequired {
		t.Fatalf("expected confirmation error, got %v", err)
	}
}
