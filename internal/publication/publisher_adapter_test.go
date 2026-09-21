package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/publisher"
)

type fakeLegacyPublisher struct {
	platform   domain.Platform
	configured bool
	result     domain.PublicationResult
	publishErr error
	calls      atomic.Int32
	lastReq    domain.PublishProjectRequest
}

func (f *fakeLegacyPublisher) Platform() domain.Platform { return f.platform }

func (f *fakeLegacyPublisher) Capability(context.Context) domain.PlatformPublishCapability {
	return domain.PlatformPublishCapability{
		Platform:      f.platform,
		Configured:    f.configured,
		Description:   "fake publisher capability",
		SupportsVideo: true,
	}
}

func (f *fakeLegacyPublisher) Publish(_ context.Context, req domain.PublishProjectRequest) (domain.PublicationResult, error) {
	f.calls.Add(1)
	f.lastReq = req
	if f.publishErr != nil {
		return domain.PublicationResult{}, f.publishErr
	}
	result := f.result
	if result.Platform == "" {
		result.Platform = f.platform
	}
	return result, nil
}

func TestPublisherAdapterAllowlistAndCapabilityPassthrough(t *testing.T) {
	configured := &fakeLegacyPublisher{platform: domain.PlatformDouyin, configured: true}
	adapter, err := NewPublisherAdapter(configured)
	if err != nil {
		t.Fatal(err)
	}
	capability := adapter.Capability(context.Background())
	if capability.Platform != PlatformDouyin || !capability.Configured {
		t.Fatalf("configured capability was not passed through: %+v", capability)
	}

	unconfigured := &fakeLegacyPublisher{platform: domain.PlatformXiaohongshu, configured: false}
	unconfiguredAdapter, err := NewPublisherAdapter(unconfigured)
	if err != nil {
		t.Fatal(err)
	}
	unconfiguredCapability := unconfiguredAdapter.Capability(context.Background())
	if unconfiguredCapability.Configured || unconfiguredCapability.Platform != PlatformXiaohongshu {
		t.Fatalf("unconfigured capability was not passed through: %+v", unconfiguredCapability)
	}

	if _, err := NewPublisherAdapter(publisher.NewWechatChannelsPublisher()); err == nil {
		t.Fatal("wechat channels must be excluded from the publication adapter allowlist")
	}
}

func TestPublisherAdapterCommitMapsPackageAndVerifiesExternalID(t *testing.T) {
	fake := &fakeLegacyPublisher{
		platform:   domain.PlatformDouyin,
		configured: true,
		result: domain.PublicationResult{
			Status:     domain.PublicationStatusSubmitted,
			ExternalID: "item-1",
		},
	}
	adapter, err := NewPublisherAdapter(fake)
	if err != nil {
		t.Fatal(err)
	}
	pkg := packageFor(t, "acct", PlatformDouyin, "run-adapter-commit")
	pkg.Title = "标题"
	pkg.Tags = []string{"tag-a", "tag-b"}
	evidence, err := adapter.Commit(context.Background(), AdapterRequest{Package: pkg})
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Verified || evidence.Status != StatusSubmitted || evidence.ExternalID != "item-1" {
		t.Fatalf("submitted result was not verified: %+v", evidence)
	}
	if !fake.lastReq.Confirm || fake.lastReq.VideoPath != pkg.Video || fake.lastReq.CoverPath != pkg.Cover || fake.lastReq.Caption != pkg.Copy || fake.lastReq.Title != pkg.Title {
		t.Fatalf("legacy request was not mapped from package: %+v", fake.lastReq)
	}
	if len(fake.lastReq.Tags) != 2 || fake.lastReq.Tags[0] != "tag-a" {
		t.Fatalf("legacy tags were not mapped: %+v", fake.lastReq.Tags)
	}
}

func TestPublisherAdapterActionRequiredAndMissingExternalIDStayUnverified(t *testing.T) {
	tests := []struct {
		name   string
		result domain.PublicationResult
	}{
		{name: "action required", result: domain.PublicationResult{Status: domain.PublicationStatusActionRequired}},
		{name: "missing external id", result: domain.PublicationResult{Status: domain.PublicationStatusSubmitted}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeLegacyPublisher{platform: domain.PlatformXiaohongshu, configured: true, result: tt.result}
			adapter, err := NewPublisherAdapter(fake)
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := adapter.Commit(context.Background(), AdapterRequest{Package: packageFor(t, "acct-"+tt.name, PlatformXiaohongshu, "run-"+tt.name)})
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := StatusUnknown
			if tt.name == "missing external id" {
				wantStatus = StatusSubmitted
			}
			if evidence.Verified || evidence.Status != wantStatus {
				t.Fatalf("result must remain unverified: %+v", evidence)
			}
		})
	}
}

func TestPublisherAdapterReconcileDoesNotPublishAgain(t *testing.T) {
	fake := &fakeLegacyPublisher{
		platform:   domain.PlatformDouyin,
		configured: true,
		result:     domain.PublicationResult{Status: domain.PublicationStatusSubmitted},
	}
	adapter, err := NewPublisherAdapter(fake)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := newTestOrchestrator(t, testNow(), adapter)
	pkg := packageFor(t, "acct", PlatformDouyin, "run-adapter-reconcile")
	a := authorize(t, o, pkg, testNow().Add(time.Hour))
	prepared := prepare(t, o, pkg, a)
	if _, err := o.Commit(context.Background(), CommitRequest{PublicationID: prepared.ID, Fingerprint: prepared.Fingerprint, Confirm: true}); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("expected missing external evidence error, got %v", err)
	}
	if _, err := o.Reconcile(context.Background(), ReconcileRequest{PublicationID: prepared.ID}); !errors.Is(err, ErrReconcilePending) {
		t.Fatalf("expected pending reconcile error, got %v", err)
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("reconcile must not call legacy Publish again, calls=%d", fake.calls.Load())
	}
}

func TestPackageFingerprintIncludesStructuredPublishingCopyFields(t *testing.T) {
	pkg := packageFor(t, "acct", PlatformDouyin, "run-copy-fingerprint")
	base, err := pkg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	withTitle := pkg
	withTitle.Title = "标题 A"
	titleFingerprint, err := withTitle.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if titleFingerprint == base {
		t.Fatal("title changes must invalidate the publication fingerprint")
	}
	withTags := withTitle
	withTags.Tags = []string{"tag-a"}
	tagsFingerprint, err := withTags.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if tagsFingerprint == titleFingerprint {
		t.Fatal("tag changes must invalidate the publication fingerprint")
	}
}

func TestPackageFingerprintIncludesEveryBoundCoverAsset(t *testing.T) {
	pkg := packageFor(t, "acct", PlatformDouyin, "run-cover-fingerprint")
	portraitPath := filepath.Join(t.TempDir(), "cover-portrait-3x4.png")
	portrait := []byte("portrait-cover")
	if err := os.WriteFile(portraitPath, portrait, 0o600); err != nil {
		t.Fatal(err)
	}
	pkg.CoverAssets = []CoverAsset{{Role: "portrait_3x4", Path: portraitPath, SHA256: digest(portrait)}}
	base, err := pkg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portraitPath, []byte("changed-cover"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := pkg
	changed.CoverAssets = []CoverAsset{{Role: "portrait_3x4", Path: portraitPath, SHA256: digest([]byte("changed-cover"))}}
	next, err := changed.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if next == base {
		t.Fatal("changing a bound platform cover must invalidate the fingerprint")
	}
}

func TestStructuredPublishingCopyFieldsRoundTripThroughMigration(t *testing.T) {
	o, _ := newTestOrchestrator(t, testNow(), &fakeAdapter{platform: PlatformDouyin, configured: true})
	pkg := packageFor(t, "acct", PlatformDouyin, "run-copy-migration")
	pkg.Title = "标题"
	pkg.Tags = []string{"tag-a", "tag-b"}
	a := authorize(t, o, pkg, testNow().Add(time.Hour))
	prepared := prepare(t, o, pkg, a)
	loaded, err := o.Get(context.Background(), prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Package.Title != pkg.Title || len(loaded.Package.Tags) != 2 || loaded.Package.Tags[1] != "tag-b" {
		t.Fatalf("structured copy fields did not survive package migration: %+v", loaded.Package)
	}
}

func testNow() time.Time {
	return time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
}
