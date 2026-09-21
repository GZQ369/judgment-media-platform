package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

// RunReader is the deliberately small durable boundary used by publication.
// Distribution and command packages do not leak into this package.
type RunReader interface {
	GetRun(context.Context, string) (domain.Run, error)
}

type CoordinationOptions struct {
	Clock            func() time.Time
	MaxAuthorization time.Duration
	MaxPackageBytes  int64
}

type Service struct {
	runs             RunReader
	orchestrator     *Orchestrator
	now              func() time.Time
	maxAuthorization time.Duration
	maxPackageBytes  int64
}

type CandidateRequest struct {
	RunID     string   `json:"run_id"`
	Creator   string   `json:"creator"`
	Account   string   `json:"account"`
	Platform  Platform `json:"platform"`
	VariantID string   `json:"variant_id,omitempty"`
}

type Candidate struct {
	Request     CandidateRequest `json:"request"`
	Package     Package          `json:"package"`
	Fingerprint string           `json:"fingerprint"`
}

type AuthorizeCandidateRequest struct {
	CandidateRequest
	Confirm       bool       `json:"confirm"`
	PolicyVersion string     `json:"policy_version"`
	Selected      []Platform `json:"selected"`
	ExpiresAt     time.Time  `json:"expires_at"`
}

type PrepareCandidateRequest struct {
	CandidateRequest
	AuthorizationID string     `json:"authorization_id"`
	Fingerprint     string     `json:"fingerprint"`
	PolicyVersion   string     `json:"policy_version"`
	Selected        []Platform `json:"selected"`
	BusinessDay     string     `json:"business_day,omitempty"`
}

var (
	ErrCandidateNotReady = errors.New("publication candidate requires media_ready run")
	ErrArtifactMissing   = errors.New("publication candidate artifact is missing")
	ErrCandidateChanged  = errors.New("publication candidate changed")
	ErrCandidateInvalid  = errors.New("publication candidate has incomplete platform content")
)

func NewService(runs RunReader, orchestrator *Orchestrator, opts CoordinationOptions) (*Service, error) {
	if runs == nil || orchestrator == nil {
		return nil, errors.New("publication runs and orchestrator are required")
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.MaxAuthorization == 0 {
		opts.MaxAuthorization = 15 * time.Minute
	}
	if opts.MaxAuthorization <= 0 {
		return nil, errors.New("maximum authorization lifetime must be positive")
	}
	if opts.MaxPackageBytes == 0 {
		opts.MaxPackageBytes = 4 << 20
	}
	if opts.MaxPackageBytes <= 0 {
		return nil, errors.New("maximum package size must be positive")
	}
	return &Service{runs: runs, orchestrator: orchestrator, now: opts.Clock, maxAuthorization: opts.MaxAuthorization, maxPackageBytes: opts.MaxPackageBytes}, nil
}

func (s *Service) Candidate(ctx context.Context, req CandidateRequest) (Candidate, error) {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.Creator) == "" || strings.TrimSpace(req.Account) == "" || !isSupportedPlatform(req.Platform) {
		return Candidate{}, fmt.Errorf("candidate requires run_id, creator, account and supported platform")
	}
	run, err := s.runs.GetRun(ctx, req.RunID)
	if err != nil {
		return Candidate{}, err
	}
	if run.Status != domain.RunStatusMediaReady {
		return Candidate{}, ErrCandidateNotReady
	}
	if !runTargetsPlatform(run, req.Platform, req.Account) {
		return Candidate{}, fmt.Errorf("%w: target platform/account mismatch", ErrCandidateChanged)
	}
	packagePath, video := "", domain.Artifact{}
	coverArtifacts := make(map[string]domain.Artifact)
	for _, a := range run.Artifacts {
		switch {
		case a.Kind == "package" || a.ID == "publish_package" || filepath.Base(a.Path) == "publish_package.json":
			if packagePath == "" {
				packagePath = a.Path
			}
		case (a.ID == "rendered-video" && (a.Kind == "video" || a.Kind == "rendered-video")) || (a.Kind == "rendered-video"):
			if video.Path == "" {
				video = a
			}
		case a.Kind == "cover" || a.Kind == "cover-image" || a.Kind == "thumbnail" || a.ID == "cover" || a.ID == "cover-image" || a.ID == "thumbnail" || strings.HasPrefix(a.ID, "cover-"):
			if role := coverRole(a); role != "" && a.Path != "" {
				if _, exists := coverArtifacts[role]; !exists {
					coverArtifacts[role] = a
				}
			}
		}
	}
	if packagePath == "" {
		return Candidate{}, fmt.Errorf("%w: publish_package.json", ErrArtifactMissing)
	}
	if video.Path == "" {
		return Candidate{}, fmt.Errorf("%w: rendered video", ErrArtifactMissing)
	}
	if len(coverArtifacts) == 0 {
		return Candidate{}, fmt.Errorf("%w: cover", ErrArtifactMissing)
	}
	raw, err := readBounded(packagePath, s.maxPackageBytes)
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: publish package: %v", ErrArtifactMissing, err)
	}
	var project domain.VideoProject
	if err := json.Unmarshal(raw, &project); err != nil {
		return Candidate{}, fmt.Errorf("decode publish package: %w", err)
	}
	variant, err := chooseVariant(project.Variants, req.VariantID)
	if err != nil {
		return Candidate{}, err
	}
	copyText, title := "", ""
	var tags []string
	if variant != nil {
		copyText, title = variant.Caption, variant.Title
	}
	if project.Brief != nil {
		if copyText == "" {
			copyText = project.Brief.Caption
		}
		if title == "" && len(project.Brief.TitleOptions) > 0 {
			title = project.Brief.TitleOptions[0]
		}
		tags = append([]string(nil), project.Brief.Tags...)
	}
	if strings.TrimSpace(copyText) == "" {
		return Candidate{}, fmt.Errorf("%w: copy is required for %s", ErrCandidateInvalid, req.Platform)
	}
	if strings.TrimSpace(title) == "" {
		return Candidate{}, fmt.Errorf("%w: title is required for %s", ErrCandidateInvalid, req.Platform)
	}
	variantID := req.VariantID
	if variant != nil {
		variantID = variant.ID
	}
	if variantID == "" {
		variantID = "default"
	}
	atomRevision := "1"
	if run.Request.Atom != nil {
		atomRevision = fmt.Sprint(run.Request.Atom.Revision)
	}
	videoSHA, err := fileSHA(video.Path)
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: rendered video: %v", ErrArtifactMissing, err)
	}
	coverAssets, err := buildCoverAssets(coverArtifacts)
	if err != nil {
		return Candidate{}, err
	}
	selectedCover, ok := selectCoverAsset(req.Platform, coverAssets)
	if !ok {
		return Candidate{}, fmt.Errorf("%w: no usable cover for %s", ErrArtifactMissing, req.Platform)
	}
	pkg := Package{Creator: req.Creator, Account: req.Account, Platform: req.Platform, Run: req.RunID, AtomRevision: atomRevision, VariantID: variantID, Video: video.Path, VideoSHA256: videoSHA, Cover: selectedCover.Path, CoverSHA256: selectedCover.SHA256, CoverAssets: coverAssets, Copy: copyText, Title: title, Tags: tags}
	pkg.CopySHA256 = digestString(copyText)
	fp, err := pkg.Fingerprint()
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{Request: req, Package: pkg, Fingerprint: fp}, nil
}

func coverRole(artifact domain.Artifact) string {
	id := strings.ToLower(strings.TrimSpace(artifact.ID))
	base := strings.ToLower(filepath.Base(artifact.Path))
	switch {
	case id == "cover-portrait-3x4" || base == "cover-portrait-3x4.png":
		return "portrait_3x4"
	case id == "cover-landscape-4x3" || base == "cover-landscape-4x3.png":
		return "landscape_4x3"
	case id == "cover" || id == "cover-image" || id == "thumbnail" || base == "cover.png" || base == "thumbnail.png":
		return "general"
	default:
		return ""
	}
}

func buildCoverAssets(artifacts map[string]domain.Artifact) ([]CoverAsset, error) {
	roles := make([]string, 0, len(artifacts))
	for role := range artifacts {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	assets := make([]CoverAsset, 0, len(roles))
	for _, role := range roles {
		artifact := artifacts[role]
		digest, err := fileSHA(artifact.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: cover %s: %v", ErrArtifactMissing, role, err)
		}
		assets = append(assets, CoverAsset{Role: role, Path: artifact.Path, SHA256: digest})
	}
	return assets, nil
}

func selectCoverAsset(platform Platform, assets []CoverAsset) (CoverAsset, bool) {
	wanted := "general"
	if platform == PlatformBilibili {
		wanted = "landscape_4x3"
	} else if platform == PlatformDouyin || platform == PlatformXiaohongshu {
		wanted = "portrait_3x4"
	}
	for _, asset := range assets {
		if asset.Role == wanted {
			return asset, true
		}
	}
	if len(assets) > 0 {
		return assets[0], true
	}
	return CoverAsset{}, false
}

func (s *Service) Authorize(ctx context.Context, req AuthorizeCandidateRequest) (Authorization, error) {
	if !req.Confirm {
		return Authorization{}, errors.New("explicit confirmation is required")
	}
	if !req.ExpiresAt.After(s.now()) || req.ExpiresAt.After(s.now().Add(s.maxAuthorization)) {
		return Authorization{}, ErrAuthorizationExpired
	}
	candidate, err := s.Candidate(ctx, req.CandidateRequest)
	if err != nil {
		return Authorization{}, err
	}
	policy := Policy{Version: req.PolicyVersion, Selected: req.Selected}
	policy, err = policy.normalized()
	if err != nil || !policy.allows(candidate.Package.Platform) {
		if err == nil {
			err = errors.New("policy does not select candidate platform")
		}
		return Authorization{}, err
	}
	return s.orchestrator.Authorize(ctx, AuthorizeRequest{Fingerprint: candidate.Fingerprint, PolicyVersion: policy.Version, Creator: candidate.Package.Creator, Account: candidate.Package.Account, Platform: candidate.Package.Platform, Run: candidate.Package.Run, VariantID: candidate.Package.VariantID, ExpiresAt: req.ExpiresAt})
}

func (s *Service) Prepare(ctx context.Context, req PrepareCandidateRequest) (Publication, error) {
	candidate, err := s.Candidate(ctx, req.CandidateRequest)
	if err != nil {
		return Publication{}, err
	}
	if !strings.EqualFold(candidate.Fingerprint, req.Fingerprint) {
		return Publication{}, ErrCandidateChanged
	}
	policy := Policy{Version: req.PolicyVersion, Selected: req.Selected}
	return s.orchestrator.Prepare(ctx, PrepareRequest{Package: candidate.Package, Policy: policy, AuthorizationID: req.AuthorizationID, Fingerprint: candidate.Fingerprint, BusinessDay: req.BusinessDay})
}

func (s *Service) Commit(ctx context.Context, req CommitRequest) (Publication, error) {
	return s.orchestrator.Commit(ctx, req)
}
func (s *Service) Reconcile(ctx context.Context, req ReconcileRequest) (Publication, error) {
	return s.orchestrator.Reconcile(ctx, req)
}
func (s *Service) Get(ctx context.Context, id string) (Publication, error) {
	return s.orchestrator.Get(ctx, id)
}
func (s *Service) List(ctx context.Context, limit int) ([]Publication, error) {
	return s.orchestrator.List(ctx, limit)
}
func (s *Service) Capabilities(ctx context.Context) []Capability {
	return s.orchestrator.Capabilities(ctx)
}

func runTargetsPlatform(run domain.Run, platform Platform, account string) bool {
	if len(run.Request.Targets) > 0 {
		for _, target := range run.Request.Targets {
			if Platform(target.Platform) == platform && (target.AccountID == "" || target.AccountID == account) {
				return true
			}
		}
		return false
	}
	return Platform(run.Request.Platform) == platform
}

func chooseVariant(variants []domain.Variant, id string) (*domain.Variant, error) {
	if id != "" {
		for i := range variants {
			if variants[i].ID == id {
				return &variants[i], nil
			}
		}
		return nil, fmt.Errorf("variant %q not found", id)
	}
	if len(variants) == 0 {
		return nil, nil
	}
	return &variants[0], nil
}

func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("publish package exceeds size limit")
	}
	return data, nil
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
