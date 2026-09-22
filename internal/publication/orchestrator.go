package publication

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
)

// MediaVerifier must verify actual bytes against both declared media digests.
// A remote immutable object store can inject its own verifier; the default reads local files.
type MediaVerifier func(context.Context, Package) error
type Options struct {
	Clock            func() time.Time
	Location         *time.Location
	OperationTimeout time.Duration
	VerifyMedia      MediaVerifier
}
type Orchestrator struct {
	db       *sql.DB
	adapters map[Platform]Adapter
	now      func() time.Time
	location *time.Location
	timeout  time.Duration
	verify   MediaVerifier
}

func New(db *sql.DB, adapters ...Adapter) (*Orchestrator, error) {
	return NewWithOptions(db, Options{}, adapters...)
}
func NewWithClock(db *sql.DB, now func() time.Time, adapters ...Adapter) (*Orchestrator, error) {
	return NewWithOptions(db, Options{Clock: now}, adapters...)
}
func NewWithOptions(db *sql.DB, opts Options, adapters ...Adapter) (*Orchestrator, error) {
	if db == nil {
		return nil, errors.New("publication database is required")
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Location == nil {
		var err error
		opts.Location, err = time.LoadLocation("Asia/Shanghai")
		if err != nil {
			return nil, err
		}
	}
	if opts.OperationTimeout == 0 {
		opts.OperationTimeout = 2 * time.Minute
	}
	if opts.OperationTimeout < 0 {
		return nil, errors.New("operation timeout must be positive")
	}
	if opts.VerifyMedia == nil {
		opts.VerifyMedia = VerifyLocalMedia
	}
	o := &Orchestrator{db: db, now: opts.Clock, location: opts.Location, timeout: opts.OperationTimeout, verify: opts.VerifyMedia, adapters: map[Platform]Adapter{}}
	for _, a := range adapters {
		if a == nil {
			continue
		}
		if !isSupportedPlatform(a.Platform()) {
			return nil, errors.New("unsupported adapter platform")
		}
		if o.adapters[a.Platform()] != nil {
			return nil, errors.New("duplicate adapter")
		}
		o.adapters[a.Platform()] = a
	}
	if err := o.Migrate(context.Background()); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Orchestrator) Authorize(ctx context.Context, r AuthorizeRequest) (Authorization, error) {
	fingerprint := strings.ToLower(r.Fingerprint)
	if !isSHA256(fingerprint) || r.Creator == "" || r.Account == "" || r.Run == "" || r.VariantID == "" || r.PolicyVersion == "" || !isSupportedPlatform(r.Platform) {
		return Authorization{}, ErrAuthorizationMismatch
	}
	if !r.ExpiresAt.After(o.now()) {
		return Authorization{}, ErrAuthorizationExpired
	}
	a := Authorization{ID: newID(), Fingerprint: fingerprint, PolicyVersion: r.PolicyVersion, Creator: r.Creator, Account: r.Account, Platform: r.Platform, Run: r.Run, VariantID: r.VariantID, ExpiresAt: r.ExpiresAt.UTC()}
	err := o.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO pub_authorizations(id,fingerprint,policy_version,creator,account,platform,run_id,variant_id,expires_at) VALUES(?,?,?,?,?,?,?,?,?)`, a.ID, a.Fingerprint, a.PolicyVersion, a.Creator, a.Account, a.Platform, a.Run, a.VariantID, formatTime(a.ExpiresAt))
		return err
	})
	return a, err
}

func (o *Orchestrator) checkAuth(ctx context.Context, tx *sql.Tx, p Publication) error {
	var a Authorization
	var expires string
	var consumed sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,policy_version,creator,account,platform,run_id,variant_id,expires_at,consumed_at FROM pub_authorizations WHERE id=?`, p.AuthorizationID).Scan(&a.Fingerprint, &a.PolicyVersion, &a.Creator, &a.Account, &a.Platform, &a.Run, &a.VariantID, &expires, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthorizationMismatch
	}
	if err != nil {
		return err
	}
	pkg := p.Package
	if !strings.EqualFold(a.Fingerprint, p.Fingerprint) || a.PolicyVersion != p.PolicyVersion || a.Creator != pkg.Creator || a.Account != pkg.Account || a.Platform != pkg.Platform || a.Run != pkg.Run || a.VariantID != pkg.VariantID {
		return ErrAuthorizationMismatch
	}
	if consumed.Valid {
		return ErrAuthorizationConsumed
	}
	expiry, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return err
	}
	if !expiry.After(o.now()) {
		return ErrAuthorizationExpired
	}
	return nil
}

func (o *Orchestrator) Prepare(ctx context.Context, r PrepareRequest) (Publication, error) {
	pkg := r.Package
	fp, err := pkg.Fingerprint()
	if err != nil {
		return Publication{}, err
	}
	if !strings.EqualFold(fp, r.Fingerprint) {
		return Publication{}, ErrAuthorizationMismatch
	}
	pkg.VideoSHA256 = strings.ToLower(pkg.VideoSHA256)
	pkg.CoverSHA256 = strings.ToLower(pkg.CoverSHA256)
	pkg.CopySHA256, err = copyDigest(pkg.Copy, pkg.CopySHA256)
	if err != nil {
		return Publication{}, err
	}
	policy, err := r.Policy.normalized()
	if err != nil {
		return Publication{}, err
	}
	if !policy.allows(pkg.Platform) {
		return Publication{}, errors.New("platform not selected")
	}
	sort.Slice(policy.Selected, func(i, j int) bool { return policy.Selected[i] < policy.Selected[j] })
	if _, err = o.adapter(ctx, pkg.Platform); err != nil {
		return Publication{}, err
	}
	if err = o.verify(ctx, pkg); err != nil {
		return Publication{}, err
	}
	p := Publication{ID: newID(), Package: pkg, Fingerprint: fp, PolicyVersion: policy.Version, AuthorizationID: r.AuthorizationID, Status: StatusDraftReady}
	err = o.withTx(ctx, func(tx *sql.Tx) error {
		now := o.now()
		day := now.In(o.location).Format("2006-01-02")
		if r.BusinessDay != "" && r.BusinessDay != day {
			return ErrBusinessDayMismatch
		}
		p.BusinessDay = day
		p.CreatedAt = now.UTC()
		p.UpdatedAt = p.CreatedAt
		if err := o.checkAuth(ctx, tx, p); err != nil {
			return err
		}
		policyJSON, _ := json.Marshal(policy)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO pub_policies VALUES(?,?)`, policy.Version, string(policyJSON)); err != nil {
			return err
		}
		var savedPolicy string
		if err := tx.QueryRowContext(ctx, `SELECT policy_json FROM pub_policies WHERE version=?`, policy.Version).Scan(&savedPolicy); err != nil {
			return err
		}
		if savedPolicy != string(policyJSON) {
			return errors.New("policy version already bound to different selections")
		}
		var run, atom string
		err := tx.QueryRowContext(ctx, `SELECT id,atom_revision FROM pub_runs WHERE creator=? AND business_day=?`, pkg.Creator, day).Scan(&run, &atom)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err = tx.ExecContext(ctx, `INSERT INTO pub_runs VALUES(?,?,?,?)`, pkg.Creator, pkg.Run, day, pkg.AtomRevision); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if run != pkg.Run {
			return &DailyContentError{Creator: pkg.Creator, Day: day, Run: run}
		} else if atom != pkg.AtomRevision {
			return errors.New("run atom revision changed")
		}
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT id FROM pub_publications WHERE creator=? AND run_id=? AND platform=? AND account=? AND variant_id=?`, pkg.Creator, pkg.Run, pkg.Platform, pkg.Account, pkg.VariantID).Scan(&existing)
		if err == nil {
			saved, e := loadPublication(ctx, tx, existing)
			if e != nil {
				return e
			}
			if saved.Fingerprint != fp || saved.AuthorizationID != r.AuthorizationID || saved.PolicyVersion != policy.Version {
				return ErrAuthorizationMismatch
			}
			p = saved
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw, _ := json.Marshal(pkg)
		_, err = tx.ExecContext(ctx, `INSERT INTO pub_publications(id,creator,run_id,account,platform,variant_id,package_json,fingerprint,policy_version,business_day,authorization_id,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, pkg.Creator, pkg.Run, pkg.Account, pkg.Platform, pkg.VariantID, string(raw), fp, p.PolicyVersion, day, p.AuthorizationID, p.Status, formatTime(now), formatTime(now))
		return err
	})
	return p, err
}

func (o *Orchestrator) Commit(ctx context.Context, r CommitRequest) (Publication, error) {
	if !r.Confirm {
		return Publication{}, ErrCommitConfirmationRequired
	}
	p, err := o.Get(ctx, r.PublicationID)
	if err != nil {
		return p, err
	}
	if !strings.EqualFold(p.Fingerprint, r.Fingerprint) {
		return p, ErrAuthorizationMismatch
	}
	if p.Status != StatusDraftReady {
		return p, ErrNeedsReconcile
	}
	a, err := o.adapter(ctx, p.Package.Platform)
	if err != nil {
		return p, err
	}
	if err = o.verify(ctx, p.Package); err != nil {
		return p, err
	}
	token := newID()
	err = o.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = loadPublication(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if p.Status != StatusDraftReady {
			return ErrNeedsReconcile
		}
		if !strings.EqualFold(p.Fingerprint, r.Fingerprint) {
			return ErrAuthorizationMismatch
		}
		if p.BusinessDay != o.now().In(o.location).Format("2006-01-02") {
			return ErrBusinessDayMismatch
		}
		if err = o.checkAuth(ctx, tx, p); err != nil {
			return err
		}
		now := o.now()
		p.ReservedAt = now.UTC()
		p.ReservationToken = token
		p.operationToken = token
		p.leaseUntil = now.Add(o.timeout).UnixNano()
		p.Status = StatusCommitting
		result, err := tx.ExecContext(ctx, `UPDATE pub_authorizations SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, formatTime(now), p.AuthorizationID)
		if err != nil {
			return err
		}
		consumed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if consumed != 1 {
			return ErrAuthorizationConsumed
		}
		result, err = tx.ExecContext(ctx, `UPDATE pub_publications SET status=?,reservation_token=?,operation_token=?,lease_until=?,reserved_at=?,updated_at=? WHERE id=? AND status=?`, p.Status, token, token, p.leaseUntil, formatTime(now), formatTime(now), p.ID, StatusDraftReady)
		if err != nil {
			return err
		}
		reserved, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if reserved != 1 {
			return ErrNeedsReconcile
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	evidence, callErr := o.call(ctx, a, p, false)
	return o.finish(p, evidence, callErr, false)
}

// Reconcile owns an exclusive expiring query lease. An expired committing lease
// becomes unknown before the read-only adapter query; Commit is never repeated.
func (o *Orchestrator) Reconcile(ctx context.Context, r ReconcileRequest) (Publication, error) {
	p, err := o.Get(ctx, r.PublicationID)
	if err != nil {
		return p, err
	}
	if p.Status == StatusPublished || p.Status == StatusRejected || p.Status == StatusFailed {
		return p, nil
	}
	a, err := o.adapter(ctx, p.Package.Platform)
	if err != nil {
		return p, err
	}
	err = o.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		p, err = loadPublication(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if p.Status == StatusDraftReady || p.Status == StatusPublished || p.Status == StatusRejected || p.Status == StatusFailed {
			return &StateError{ID: p.ID, Status: p.Status}
		}
		now := o.now()
		if p.operationToken != "" && p.leaseUntil > now.UnixNano() {
			return ErrOperationActive
		}
		if p.Status == StatusCommitting {
			p.Status = StatusUnknown
		}
		p.operationToken = newID()
		p.leaseUntil = now.Add(o.timeout).UnixNano()
		_, err = tx.ExecContext(ctx, `UPDATE pub_publications SET status=?,operation_token=?,lease_until=?,updated_at=? WHERE id=?`, p.Status, p.operationToken, p.leaseUntil, formatTime(now), p.ID)
		return err
	})
	if err != nil {
		return p, err
	}
	evidence, callErr := o.call(ctx, a, p, true)
	return o.finish(p, evidence, callErr, true)
}

func (o *Orchestrator) call(ctx context.Context, a Adapter, p Publication, reconcile bool) (e Evidence, err error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("adapter panic: %v", recovered)
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	reservation := p.ReservationToken
	// ReservationToken is the stable adapter reference for the whole
	// publication attempt. operationToken is only a short-lived local query
	// lease used to fence concurrent reconciliation and must never cross the
	// adapter boundary as an external task-space reference.
	req := AdapterRequest{PublicationID: p.ID, Reservation: reservation, Package: p.Package}
	if reconcile {
		return a.Reconcile(ctx, req)
	}
	return a.Commit(ctx, req)
}

func (o *Orchestrator) finish(p Publication, e Evidence, callErr error, reconcile bool) (Publication, error) {
	status, valid := evidenceStatus(e)
	outcome := callErr
	if callErr != nil {
		outcome = fmt.Errorf("%w: %v", ErrUnknownOutcome, callErr)
	} else if !valid {
		outcome = ErrInvalidEvidence
		if reconcile {
			outcome = ErrReconcilePending
		}
	}
	if outcome != nil {
		status = StatusUnknown
		if reconcile && (p.Status == StatusSubmitted || p.Status == StatusReviewing) {
			status = p.Status
		}
	}
	// A delayed query may not regress a previously verified reviewing state.
	if p.Status == StatusReviewing && status == StatusSubmitted {
		status = StatusReviewing
	}
	raw, _ := json.Marshal(e)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := o.withTx(ctx, func(tx *sql.Tx) error {
		message := ""
		if outcome != nil {
			message = outcome.Error()
		}
		externalID, externalURL, evidence := p.ExternalID, p.ExternalURL, p.Evidence
		if outcome == nil {
			if e.ExternalID != "" {
				externalID = e.ExternalID
			}
			if e.URL != "" {
				externalURL = e.URL
			}
			evidence = string(raw)
		}
		result, err := tx.ExecContext(ctx, `UPDATE pub_publications SET status=?,external_id=?,external_url=?,evidence=?,last_error=?,operation_token='',lease_until=0,updated_at=? WHERE id=? AND operation_token=?`, status, externalID, externalURL, evidence, message, formatTime(o.now()), p.ID, p.operationToken)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStaleOperation
		}
		return nil
	})
	if err != nil {
		return p, errors.Join(ErrUnknownOutcome, err)
	}
	saved, err := o.Get(ctx, p.ID)
	return saved, errors.Join(outcome, err)
}

// Get also recovers expired committing leases; it never invokes an adapter.
func (o *Orchestrator) Get(ctx context.Context, id string) (Publication, error) {
	var p Publication
	err := o.withTx(ctx, func(tx *sql.Tx) error {
		now := o.now()
		_, err := tx.ExecContext(ctx, `UPDATE pub_publications SET status='unknown',operation_token='',lease_until=0,last_error='operation lease expired',updated_at=? WHERE id=? AND status='committing' AND lease_until<=?`, formatTime(now), id, now.UnixNano())
		if err != nil {
			return err
		}
		p, err = loadPublication(ctx, tx, id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Publication{}, ErrNotFound
	}
	return p, err
}

// GetForAnalytics reads the durable publication without lease recovery or any
// other state transition. Scheduled analytics must use this read-only path.
func (o *Orchestrator) GetForAnalytics(ctx context.Context, id string) (Publication, error) {
	return loadPublication(ctx, o.db, id)
}

// List returns durable publication snapshots without exposing reservation
// tokens through JSON (Publication deliberately tags that field as private).
func (o *Orchestrator) List(ctx context.Context, limit int) ([]Publication, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := o.db.QueryContext(ctx, `SELECT id FROM pub_publications ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]Publication, 0, len(ids))
	for _, id := range ids {
		item, err := o.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ListForAnalytics returns only reserved publication snapshots in stable ID
// order. It is deliberately a direct read: unlike Get, it never recovers an
// expired operation lease or otherwise mutates publication state.
func (o *Orchestrator) ListForAnalytics(ctx context.Context, afterID string, limit int) ([]Publication, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := o.db.QueryContext(ctx, publicationSelect+` WHERE id > ? AND reserved_at <> '' AND status <> ? ORDER BY id ASC LIMIT ?`, afterID, StatusDraftReady, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Publication, 0, limit)
	for rows.Next() {
		item, err := scanPublication(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// Capabilities is read-only and never invokes a platform operation.
func (o *Orchestrator) Capabilities(ctx context.Context) []Capability {
	items := make([]Capability, 0, len(supportedPlatforms))
	for _, platform := range supportedPlatforms {
		adapter := o.adapters[platform]
		if adapter == nil {
			items = append(items, Capability{Platform: platform, Configured: false, Reason: "no adapter registered"})
			continue
		}
		capability := adapter.Capability(ctx)
		if capability.Platform == "" {
			capability.Platform = platform
		}
		items = append(items, capability)
	}
	return items
}
func (o *Orchestrator) adapter(ctx context.Context, p Platform) (Adapter, error) {
	a := o.adapters[p]
	if a == nil {
		return nil, &CapabilityBlocker{Platform: p, Reason: "no adapter registered"}
	}
	c := a.Capability(ctx)
	if !c.Configured || c.Platform != p {
		return nil, &CapabilityBlocker{Platform: p, Reason: c.Reason}
	}
	return a, nil
}
func validatePackage(p Package) error {
	if p.Creator == "" || p.Account == "" || p.Run == "" || p.AtomRevision == "" || p.VariantID == "" || p.Video == "" || !isSupportedPlatform(p.Platform) {
		return errors.New("package requires creator/account/platform/run/atom revision/variant/video")
	}
	if !isSHA256(p.VideoSHA256) {
		return errors.New("video SHA256 is required")
	}
	if p.Cover == "" || !isSHA256(p.CoverSHA256) {
		return errors.New("cover reference and SHA256 are required")
	}
	seenCoverRoles := make(map[string]struct{}, len(p.CoverAssets))
	for _, asset := range p.CoverAssets {
		role := strings.TrimSpace(asset.Role)
		if role == "" || strings.TrimSpace(asset.Path) == "" || !isSHA256(asset.SHA256) {
			return errors.New("cover assets require role, path and SHA256")
		}
		if _, exists := seenCoverRoles[role]; exists {
			return fmt.Errorf("duplicate cover asset role %q", role)
		}
		seenCoverRoles[role] = struct{}{}
	}
	_, err := copyDigest(p.Copy, p.CopySHA256)
	return err
}
func VerifyLocalMedia(ctx context.Context, p Package) error {
	mediaFiles := []struct{ path, digest string }{{p.Video, p.VideoSHA256}, {p.Cover, p.CoverSHA256}}
	for _, asset := range p.CoverAssets {
		mediaFiles = append(mediaFiles, struct{ path, digest string }{asset.Path, asset.SHA256})
	}
	for _, media := range mediaFiles {
		if media.path == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := os.Open(media.path)
		if err != nil {
			return fmt.Errorf("verify media: %w", err)
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), media.digest) {
			return fmt.Errorf("%w: media digest changed at %s", ErrAuthorizationMismatch, media.path)
		}
	}
	return ctx.Err()
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
