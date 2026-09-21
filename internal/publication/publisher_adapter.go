package publication

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/publisher"
)

// PublisherAdapter safely exposes the legacy publisher contract to the
// publication transaction core. It deliberately wraps one platform only.
type PublisherAdapter struct {
	publisher publisher.Publisher
	platform  Platform
}

// NewPublisherAdapter wraps a publisher supported by the publication policy.
// WeChat Channels and any other legacy publisher are rejected at construction
// time, so they cannot be accidentally admitted by a publication policy.
func NewPublisherAdapter(p publisher.Publisher) (*PublisherAdapter, error) {
	if p == nil {
		return nil, errors.New("publisher adapter: publisher is required")
	}
	platform, ok := publicationPlatform(p.Platform())
	if !ok {
		return nil, fmt.Errorf("publisher adapter: unsupported publisher platform %q", p.Platform())
	}
	return &PublisherAdapter{publisher: p, platform: platform}, nil
}

// NewPublisherAdapters wraps all supplied legacy publishers and rejects the
// complete set if any publisher is outside the publication allowlist.
func NewPublisherAdapters(publishers ...publisher.Publisher) ([]Adapter, error) {
	adapters := make([]Adapter, 0, len(publishers))
	for _, p := range publishers {
		adapter, err := NewPublisherAdapter(p)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, adapter)
	}
	return adapters, nil
}

func (a *PublisherAdapter) Platform() Platform { return a.platform }

func (a *PublisherAdapter) Capability(ctx context.Context) Capability {
	c := a.publisher.Capability(ctx)
	reason := strings.TrimSpace(c.Description)
	configured := c.Configured
	if c.Platform != "" && c.Platform != domain.Platform(a.platform) {
		configured = false
		reason = fmt.Sprintf("publisher capability platform mismatch: %q", c.Platform)
	}
	if !configured && reason == "" {
		reason = "publisher is not configured"
	}
	return Capability{Platform: a.platform, Configured: configured, Reason: reason}
}

func (a *PublisherAdapter) Commit(ctx context.Context, req AdapterRequest) (Evidence, error) {
	publishRequest := domain.PublishProjectRequest{
		Platform: domain.Platform(a.platform),
		Title:    req.Package.Title,
		Caption:  req.Package.Copy,
		Tags:     append([]string(nil), req.Package.Tags...),
		// The publication core has already consumed its one-use authorization.
		Confirm: true,
	}
	setPublishReference(&publishRequest.VideoPath, &publishRequest.VideoURL, req.Package.Video)
	setPublishReference(&publishRequest.CoverPath, &publishRequest.CoverURL, req.Package.Cover)
	result, err := a.publisher.Publish(ctx, publishRequest)
	if err != nil {
		return Evidence{}, err
	}
	return publisherEvidence(a.platform, result)
}

func setPublishReference(path, remoteURL *string, reference string) {
	parsed, err := url.Parse(reference)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		*remoteURL = reference
		return
	}
	*path = reference
}

// Reconcile intentionally never calls Publish. The legacy Publisher contract
// has no official read/query operation, so its outcome cannot be verified.
func (a *PublisherAdapter) Reconcile(context.Context, AdapterRequest) (Evidence, error) {
	return Evidence{
		Status:  StatusUnknown,
		Message: "publisher does not expose an official query capability; outcome remains unverified",
	}, nil
}

func publisherEvidence(platform Platform, result domain.PublicationResult) (Evidence, error) {
	if result.Platform != "" && Platform(result.Platform) != platform {
		return Evidence{}, fmt.Errorf("publisher returned platform %q for %s", result.Platform, platform)
	}
	evidence := Evidence{
		ExternalID: result.ExternalID,
		URL:        result.ExternalURL,
		Proof:      result.Message,
		Message:    result.Message,
	}
	switch result.Status {
	case domain.PublicationStatusSubmitted,
		domain.PublicationStatus("reviewing"),
		domain.PublicationStatus("published"):
		evidence.Status = Status(result.Status)
		evidence.Verified = evidence.ExternalID != "" || evidence.URL != ""
		if !evidence.Verified {
			evidence.Message = "publisher reported submission without an external ID or URL"
		}
		return evidence, nil
	case domain.PublicationStatusActionRequired:
		evidence.Status = StatusUnknown
		evidence.Message = "publisher requires client/user action; no platform submission was verified"
		return evidence, nil
	case domain.PublicationStatusFailed:
		return Evidence{}, fmt.Errorf("publisher reported failed publication: %s", result.Message)
	default:
		return Evidence{}, fmt.Errorf("publisher returned unsupported publication status %q", result.Status)
	}
}

func publicationPlatform(platform domain.Platform) (Platform, bool) {
	switch platform {
	case domain.PlatformDouyin, domain.PlatformXiaohongshu:
		return Platform(platform), true
	case domain.Platform("bilibili"):
		return PlatformBilibili, true
	default:
		return "", false
	}
}
