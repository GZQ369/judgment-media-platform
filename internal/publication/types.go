// Package publication contains the durable, non-browser publication
// transaction core. Platform behavior is supplied by injected adapters.
package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Platform is a platform supported by the publication policy.
type Platform string

const (
	PlatformDouyin      Platform = "douyin"
	PlatformXiaohongshu Platform = "xiaohongshu"
	PlatformBilibili    Platform = "bilibili"
)

var supportedPlatforms = []Platform{
	PlatformDouyin,
	PlatformXiaohongshu,
	PlatformBilibili,
}

// Status is the durable state of one platform submission.
type Status string

const (
	StatusDraftReady Status = "draft_ready"
	StatusCommitting Status = "committing"
	StatusSubmitted  Status = "submitted"
	StatusReviewing  Status = "reviewing"
	StatusPublished  Status = "published"
	StatusRejected   Status = "rejected"
	StatusUnknown    Status = "unknown"
	StatusFailed     Status = "failed"
)

// Package is the immutable content package used by a publication run.
// Video and Cover are opaque references (for example, paths or object URLs).
type Package struct {
	Creator      string       `json:"creator"`
	Account      string       `json:"account"`
	Platform     Platform     `json:"platform"`
	Run          string       `json:"run"`
	AtomRevision string       `json:"atom_revision"`
	VariantID    string       `json:"variant_id"`
	Video        string       `json:"video"`
	VideoSHA256  string       `json:"video_sha256"`
	Cover        string       `json:"cover"`
	CoverSHA256  string       `json:"cover_sha256"`
	CoverAssets  []CoverAsset `json:"cover_assets,omitempty"`
	Copy         string       `json:"copy"`
	CopySHA256   string       `json:"copy_sha256"`
	CopySummary  string       `json:"copy_summary"`
	Title        string       `json:"title,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
}

// CoverAsset is a concrete cover file that is bound to the publication
// transaction. Adapters must use these references only; they must not infer
// sibling files from the video path after authorization.
type CoverAsset struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Fingerprint returns the identity of the concrete platform package. Paths
// and summaries are deliberately not sufficient: media digests and complete
// copy content are included so changing a file at the same path invalidates
// the prepared publication.
func (p Package) Fingerprint() (string, error) {
	if err := validatePackage(p); err != nil {
		return "", err
	}
	copySHA, err := copyDigest(p.Copy, p.CopySHA256)
	if err != nil {
		return "", err
	}
	identity := struct {
		Creator      string       `json:"creator"`
		Account      string       `json:"account"`
		Platform     Platform     `json:"platform"`
		Run          string       `json:"run"`
		AtomRevision string       `json:"atom_revision"`
		VariantID    string       `json:"variant_id"`
		VideoSHA256  string       `json:"video_sha256"`
		CoverSHA256  string       `json:"cover_sha256"`
		CoverAssets  []CoverAsset `json:"cover_assets"`
		CopySHA256   string       `json:"copy_sha256"`
		Title        string       `json:"title"`
		Tags         []string     `json:"tags"`
	}{p.Creator, p.Account, p.Platform, p.Run, p.AtomRevision, p.VariantID, strings.ToLower(p.VideoSHA256), strings.ToLower(p.CoverSHA256), normalizedCoverAssetsForFingerprint(p.CoverAssets), copySHA, p.Title, append([]string(nil), p.Tags...)}
	b, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

// Policy selects the platforms for one content run. A zero-value policy is
// intentionally not silently expanded; callers should use DefaultPolicy.
type Policy struct {
	Version  string     `json:"version"`
	Selected []Platform `json:"selected"`
}

// DefaultPolicy is the supported default and deliberately excludes WeChat.
func DefaultPolicy(version string) Policy {
	if version == "" {
		version = "v1"
	}
	return Policy{Version: version, Selected: append([]Platform(nil), supportedPlatforms...)}
}

func (p Policy) normalized() (Policy, error) {
	if p.Version == "" {
		return Policy{}, errors.New("policy version is required")
	}
	if len(p.Selected) == 0 {
		return Policy{}, errors.New("policy must select at least one platform")
	}
	seen := make(map[Platform]struct{}, len(p.Selected))
	selected := make([]Platform, 0, len(p.Selected))
	for _, platform := range p.Selected {
		if !isSupportedPlatform(platform) {
			return Policy{}, fmt.Errorf("unsupported publication platform %q", platform)
		}
		if _, ok := seen[platform]; ok {
			continue
		}
		seen[platform] = struct{}{}
		selected = append(selected, platform)
	}
	p.Selected = selected
	return p, nil
}

func (p Policy) allows(platform Platform) bool {
	for _, selected := range p.Selected {
		if selected == platform {
			return true
		}
	}
	return false
}

// Authorization is a one-use grant bound to the exact publication identity.
type Authorization struct {
	ID            string    `json:"id"`
	Fingerprint   string    `json:"fingerprint"`
	PolicyVersion string    `json:"policy_version"`
	Creator       string    `json:"creator"`
	Account       string    `json:"account"`
	Platform      Platform  `json:"platform"`
	Run           string    `json:"run"`
	VariantID     string    `json:"variant_id"`
	ExpiresAt     time.Time `json:"expires_at"`
	ConsumedAt    time.Time `json:"consumed_at,omitempty"`
}

type AuthorizeRequest struct {
	Fingerprint   string
	PolicyVersion string
	Creator       string
	Account       string
	Platform      Platform
	Run           string
	VariantID     string
	ExpiresAt     time.Time
}

// PrepareRequest admits one content run and prepares one selected platform.
// BusinessDay only asserts the clock-derived local date (default Asia/Shanghai).
type PrepareRequest struct {
	Package         Package
	Policy          Policy
	AuthorizationID string
	Fingerprint     string
	BusinessDay     string
}

type CommitRequest struct {
	PublicationID string
	Fingerprint   string
	Confirm       bool
}

type ReconcileRequest struct {
	PublicationID string
}

// Publication is the durable transaction snapshot returned by every operation.
type Publication struct {
	ID               string    `json:"id"`
	Package          Package   `json:"package"`
	PolicyVersion    string    `json:"policy_version"`
	Fingerprint      string    `json:"fingerprint"`
	BusinessDay      string    `json:"business_day"`
	AuthorizationID  string    `json:"authorization_id"`
	Status           Status    `json:"status"`
	ReservationToken string    `json:"-"`
	ExternalID       string    `json:"external_id,omitempty"`
	ExternalURL      string    `json:"external_url,omitempty"`
	Evidence         string    `json:"evidence,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	ReservedAt       time.Time `json:"reserved_at,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	operationToken   string
	leaseUntil       int64
}

// Evidence is the only adapter output that can advance a publication past
// committing. Verified must be true and ExternalID or URL must be present for
// submitted/reviewing/published. Rejected also requires verified evidence.
type Evidence struct {
	Status     Status `json:"status"`
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	Verified   bool   `json:"verified"`
	Proof      string `json:"proof,omitempty"`
	Message    string `json:"message,omitempty"`
}

// AdapterRequest contains the reservation token, which adapters may use as
// their idempotency key. The core never retries Commit after a reservation.
type AdapterRequest struct {
	PublicationID string
	Reservation   string
	Package       Package
}

// Adapter is intentionally narrower than a browser automation interface.
// Commit and Reconcile must report platform evidence, not click/READY signals.
type Adapter interface {
	Platform() Platform
	Capability(context.Context) Capability
	Commit(context.Context, AdapterRequest) (Evidence, error)
	Reconcile(context.Context, AdapterRequest) (Evidence, error)
}

type Capability struct {
	Platform   Platform `json:"platform"`
	Configured bool     `json:"configured"`
	Reason     string   `json:"reason,omitempty"`
}

// CapabilityBlocker is returned before reservation when no configured adapter
// can perform the requested platform operation.
type CapabilityBlocker struct {
	Platform Platform
	Reason   string
}

func (e *CapabilityBlocker) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("publication capability blocker: %s is not configured", e.Platform)
	}
	return fmt.Sprintf("publication capability blocker: %s: %s", e.Platform, e.Reason)
}

var (
	ErrNotFound                   = errors.New("publication not found")
	ErrDailyContentAdmitted       = errors.New("daily content already admitted")
	ErrAuthorizationExpired       = errors.New("publication authorization expired")
	ErrAuthorizationMismatch      = errors.New("publication authorization binding mismatch")
	ErrAuthorizationConsumed      = errors.New("publication authorization already consumed")
	ErrNeedsReconcile             = errors.New("publication requires reconciliation; commit will not be retried")
	ErrUnknownOutcome             = errors.New("publication outcome is unknown")
	ErrReconcilePending           = errors.New("reconciliation has no verified platform evidence yet")
	ErrInvalidEvidence            = errors.New("adapter returned insufficient verified evidence")
	ErrBusinessDayMismatch        = errors.New("business day must be derived from the orchestrator clock and location")
	ErrOperationActive            = errors.New("publication operation is active")
	ErrStaleOperation             = errors.New("publication operation lost its lease")
	ErrCommitConfirmationRequired = errors.New("explicit commit confirmation is required")
)

type DailyContentError struct {
	Creator string
	Day     string
	Run     string
}

func (e *DailyContentError) Error() string {
	return fmt.Sprintf("%s: creator=%s day=%s existing_run=%s", ErrDailyContentAdmitted, e.Creator, e.Day, e.Run)
}

func (e *DailyContentError) Unwrap() error { return ErrDailyContentAdmitted }

type StateError struct {
	ID     string
	Status Status
}

func (e *StateError) Error() string {
	return fmt.Sprintf("publication %s is in state %s", e.ID, e.Status)
}

func isSupportedPlatform(platform Platform) bool {
	for _, supported := range supportedPlatforms {
		if supported == platform {
			return true
		}
	}
	return false
}

func evidenceStatus(e Evidence) (Status, bool) {
	if !e.Verified {
		return "", false
	}
	status := e.Status
	if status == "" {
		status = StatusSubmitted
	}
	switch status {
	case StatusSubmitted, StatusReviewing, StatusPublished:
		if e.ExternalID == "" && e.URL == "" {
			return "", false
		}
	case StatusRejected:
		if e.ExternalID == "" && e.URL == "" && e.Proof == "" {
			return "", false
		}
	default:
		return "", false
	}
	return status, true
}

func copyDigest(copyText, declared string) (string, error) {
	if copyText == "" && declared == "" {
		return "", errors.New("package requires complete copy or copy sha256")
	}
	if declared != "" && !isSHA256(declared) {
		return "", errors.New("copy sha256 must be 64 hexadecimal characters")
	}
	if copyText == "" {
		return strings.ToLower(declared), nil
	}
	digest := sha256.Sum256([]byte(copyText))
	computed := hex.EncodeToString(digest[:])
	if declared != "" && !strings.EqualFold(computed, declared) {
		return "", errors.New("copy sha256 does not match complete copy")
	}
	return computed, nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizedCoverAssetsForFingerprint(assets []CoverAsset) []CoverAsset {
	if len(assets) == 0 {
		return nil
	}
	copyAssets := append([]CoverAsset(nil), assets...)
	sort.Slice(copyAssets, func(i, j int) bool {
		if copyAssets[i].Role == copyAssets[j].Role {
			return copyAssets[i].Path < copyAssets[j].Path
		}
		return copyAssets[i].Role < copyAssets[j].Role
	})
	for i := range copyAssets {
		copyAssets[i].SHA256 = strings.ToLower(copyAssets[i].SHA256)
	}
	return copyAssets
}
