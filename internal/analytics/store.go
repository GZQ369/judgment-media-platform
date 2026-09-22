// Package analytics provides durable synchronization of publication review
// evidence and verified platform metrics.
package analytics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/GZQ369/judgment-media-platform/internal/platforminsights"
)

type Store struct{ db *sql.DB }

type SyncJob struct {
	PublicationID string    `json:"publication_id"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     string    `json:"last_error,omitempty"`
	LeaseOwner    string    `json:"-"`
	LeaseToken    string    `json:"-"`
	LeaseUntil    time.Time `json:"lease_until,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ReviewSnapshot struct {
	ID              string                             `json:"id"`
	PublicationID   string                             `json:"publication_id"`
	Provider        string                             `json:"provider"`
	Platform        string                             `json:"platform"`
	Status          platforminsights.PublicationStatus `json:"status"`
	ExternalID      string                             `json:"external_id,omitempty"`
	ExternalURL     string                             `json:"external_url,omitempty"`
	Verified        bool                               `json:"verified"`
	Proof           string                             `json:"proof,omitempty"`
	ObservationHash string                             `json:"observation_hash"`
	ObservedAt      time.Time                          `json:"observed_at"`
	LastSeenAt      time.Time                          `json:"last_seen_at"`
}

type MetricSnapshot struct {
	ID              string             `json:"id"`
	PublicationID   string             `json:"publication_id"`
	Provider        string             `json:"provider"`
	Platform        string             `json:"platform"`
	ExternalID      string             `json:"external_id"`
	ObservationHash string             `json:"observation_hash"`
	Metrics         map[string]float64 `json:"metrics"`
	Raw             map[string]any     `json:"raw,omitempty"`
	ObservedAt      time.Time          `json:"observed_at"`
	LastSeenAt      time.Time          `json:"last_seen_at"`
}

type Event struct {
	ID            int64          `json:"id"`
	PublicationID string         `json:"publication_id"`
	Type          string         `json:"type"`
	Payload       map[string]any `json:"payload,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

// Summary is a read-only aggregate over the independently persisted
// analytics database. It intentionally contains no provider secrets.
type Summary struct {
	PublicationCount int            `json:"publication_count"`
	ReviewSnapshots  int            `json:"review_snapshots"`
	MetricSnapshots  int            `json:"metric_snapshots"`
	Events           int            `json:"events"`
	JobsByStatus     map[string]int `json:"jobs_by_status"`
	LastObservedAt   *time.Time     `json:"last_observed_at,omitempty"`
}

type Lease struct {
	Owner string
	Token string
	Until time.Time
}

const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobRetry   = "retry"
	JobWaiting = "waiting"
	JobBlocked = "blocked"
	JobDone    = "done"
)

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("analytics store path is required")
	}
	db, err := sql.Open("sqlite", filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open analytics store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure analytics sqlite: %w", err)
		}
	}
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("analytics database is required")
	}
	store := &Store{db: db}
	return store, store.migrate()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS analytics_sync_jobs (
 publication_id TEXT PRIMARY KEY, status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL, last_error TEXT NOT NULL DEFAULT '',
 lease_owner TEXT NOT NULL DEFAULT '', lease_token TEXT NOT NULL DEFAULT '', lease_until TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS analytics_review_snapshots (
 id TEXT PRIMARY KEY, publication_id TEXT NOT NULL, provider TEXT NOT NULL, platform TEXT NOT NULL,
 status TEXT NOT NULL, external_id TEXT NOT NULL DEFAULT '', external_url TEXT NOT NULL DEFAULT '',
 verified INTEGER NOT NULL, proof TEXT NOT NULL DEFAULT '', observation_hash TEXT NOT NULL,
 observed_at TEXT NOT NULL, last_seen_at TEXT NOT NULL,
 UNIQUE(publication_id, provider, observation_hash)
);
CREATE INDEX IF NOT EXISTS analytics_review_publication ON analytics_review_snapshots(publication_id, observed_at);
CREATE TABLE IF NOT EXISTS analytics_metric_snapshots (
 id TEXT PRIMARY KEY, publication_id TEXT NOT NULL, provider TEXT NOT NULL, platform TEXT NOT NULL,
 external_id TEXT NOT NULL, observation_hash TEXT NOT NULL, metrics_json TEXT NOT NULL, raw_json TEXT,
 observed_at TEXT NOT NULL, last_seen_at TEXT NOT NULL,
 UNIQUE(publication_id, provider, observation_hash)
);
CREATE INDEX IF NOT EXISTS analytics_metric_publication ON analytics_metric_snapshots(publication_id, observed_at);
CREATE TABLE IF NOT EXISTS analytics_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, publication_id TEXT NOT NULL, type TEXT NOT NULL,
 payload_json TEXT, created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS analytics_events_publication ON analytics_events(publication_id, id);
CREATE TABLE IF NOT EXISTS analytics_scheduler_state (
 name TEXT PRIMARY KEY, cursor TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
);
`)
	if err != nil {
		return fmt.Errorf("migrate analytics store: %w", err)
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, publicationID string) error {
	if strings.TrimSpace(publicationID) == "" {
		return errors.New("publication id is required")
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO analytics_sync_jobs(publication_id,status,next_attempt_at,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(publication_id) DO UPDATE SET status=CASE WHEN analytics_sync_jobs.status IN (?,?,?,?) THEN ? ELSE analytics_sync_jobs.status END, next_attempt_at=CASE WHEN analytics_sync_jobs.status IN (?,?,?,?) THEN excluded.next_attempt_at ELSE analytics_sync_jobs.next_attempt_at END, updated_at=excluded.updated_at`, publicationID, JobQueued, stamp(now), stamp(now), stamp(now), JobDone, JobBlocked, JobRetry, JobWaiting, JobQueued, JobDone, JobBlocked, JobRetry, JobWaiting)
	return err
}

// EnsureScheduled makes a discovered publication schedulable without
// disturbing an existing job. In particular, a poll pass must not reset a
// running, waiting, done, or blocked job back to queued.
func (s *Store) EnsureScheduled(ctx context.Context, publicationID string) error {
	if strings.TrimSpace(publicationID) == "" {
		return errors.New("publication id is required")
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO analytics_sync_jobs(publication_id,status,next_attempt_at,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(publication_id) DO NOTHING`, publicationID, JobQueued, stamp(now), stamp(now), stamp(now))
	return err
}

// ReactivateDone moves one completed job back to the durable queue. The
// status and expired-lease predicates make repeated discovery idempotent and
// keep a live worker lease fenced from discovery.
func (s *Store) ReactivateDone(ctx context.Context, publicationID string, next time.Time) (bool, error) {
	if strings.TrimSpace(publicationID) == "" {
		return false, errors.New("publication id is required")
	}
	if next.IsZero() {
		next = time.Now().UTC()
	} else {
		next = next.UTC()
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE analytics_sync_jobs SET status=?,next_attempt_at=?,last_error='',lease_owner='',lease_token='',lease_until='',updated_at=? WHERE publication_id=? AND status=? AND (lease_until='' OR lease_until<=?)`, JobQueued, stamp(next), stamp(now), publicationID, JobDone, stamp(now))
	if err != nil {
		return false, err
	}
	count, _ := result.RowsAffected()
	return count == 1, nil
}

// DiscoveryCursor returns the last durable publication identity handed to the
// optional discovery provider. An empty cursor means the first page.
func (s *Store) DiscoveryCursor(ctx context.Context) (string, error) {
	var cursor string
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM analytics_scheduler_state WHERE name=?`, "publications").Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return cursor, err
}

// SaveDiscoveryCursor advances the cursor only after every publication in the
// discovered page has been durably scheduled.
func (s *Store) SaveDiscoveryCursor(ctx context.Context, cursor string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO analytics_scheduler_state(name,cursor,updated_at) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET cursor=excluded.cursor,updated_at=excluded.updated_at`, "publications", cursor, stamp(now))
	return err
}

func (s *Store) GetJob(ctx context.Context, publicationID string) (SyncJob, error) {
	row := s.db.QueryRowContext(ctx, `SELECT publication_id,status,attempts,next_attempt_at,last_error,lease_owner,lease_token,lease_until,created_at,updated_at FROM analytics_sync_jobs WHERE publication_id=?`, publicationID)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncJob{}, platforminsights.ErrSyncNotFound
	}
	return job, err
}

func (s *Store) ClaimDue(ctx context.Context, owner string, ttl time.Duration, now time.Time) (SyncJob, Lease, error) {
	return s.claimDue(ctx, "", owner, ttl, now)
}

// ClaimDueForPublication claims only the requested publication. This is used
// by request-scoped sync so an unrelated queued job can never be consumed.
func (s *Store) ClaimDueForPublication(ctx context.Context, publicationID, owner string, ttl time.Duration, now time.Time) (SyncJob, Lease, error) {
	if strings.TrimSpace(publicationID) == "" {
		return SyncJob{}, Lease{}, errors.New("publication id is required")
	}
	return s.claimDue(ctx, publicationID, owner, ttl, now)
}

func (s *Store) claimDue(ctx context.Context, publicationID, owner string, ttl time.Duration, now time.Time) (SyncJob, Lease, error) {
	if strings.TrimSpace(owner) == "" || ttl <= 0 {
		return SyncJob{}, Lease{}, errors.New("analytics worker owner and ttl are required")
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SyncJob{}, Lease{}, err
	}
	defer tx.Rollback()
	var job SyncJob
	query := `SELECT publication_id,status,attempts,next_attempt_at,last_error,lease_owner,lease_token,lease_until,created_at,updated_at FROM analytics_sync_jobs WHERE status IN (?,?,?,?,?) AND next_attempt_at<=? AND (lease_until='' OR lease_until<=?)`
	args := []any{JobQueued, JobRetry, JobWaiting, JobBlocked, JobRunning, stamp(now), stamp(now)}
	if publicationID != "" {
		query += ` AND publication_id=?`
		args = append(args, publicationID)
	}
	query += ` ORDER BY created_at LIMIT 1`
	row := tx.QueryRowContext(ctx, query, args...)
	if job, err = scanJob(row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SyncJob{}, Lease{}, sql.ErrNoRows
		}
		return SyncJob{}, Lease{}, err
	}
	lease := Lease{Owner: owner, Token: uuid.NewString(), Until: now.Add(ttl)}
	result, err := tx.ExecContext(ctx, `UPDATE analytics_sync_jobs SET status=?,attempts=attempts+1,lease_owner=?,lease_token=?,lease_until=?,updated_at=? WHERE publication_id=? AND status IN (?,?,?,?,?) AND (lease_until='' OR lease_until<=?)`, JobRunning, owner, lease.Token, stamp(lease.Until), stamp(now), job.PublicationID, JobQueued, JobRetry, JobWaiting, JobBlocked, JobRunning, stamp(now))
	if err != nil {
		return SyncJob{}, Lease{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return SyncJob{}, Lease{}, platforminsights.ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return SyncJob{}, Lease{}, err
	}
	job.Status = JobRunning
	job.Attempts++
	job.LeaseOwner, job.LeaseToken, job.LeaseUntil = lease.Owner, lease.Token, lease.Until
	job.UpdatedAt = now
	return job, lease, nil
}

func (s *Store) Finish(ctx context.Context, publicationID string, lease Lease, status string, next time.Time, runErr error) error {
	now := time.Now().UTC()
	lastError := ""
	if runErr != nil {
		lastError = runErr.Error()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE analytics_sync_jobs SET status=?,next_attempt_at=?,last_error=?,lease_owner='',lease_token='',lease_until='',updated_at=? WHERE publication_id=? AND lease_owner=? AND lease_token=? AND lease_until>?`, status, stamp(next.UTC()), lastError, stamp(now), publicationID, lease.Owner, lease.Token, stamp(now))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return platforminsights.ErrLeaseLost
	}
	if err := appendEventTx(ctx, tx, publicationID, "sync_"+status, map[string]any{"error": lastError}); err != nil {
		return err
	}
	return tx.Commit()
}

// Renew extends a publication sync lease only for its current fencing token.
// An expired or superseded worker cannot extend its lease.
func (s *Store) Renew(ctx context.Context, publicationID string, lease Lease, ttl time.Duration) error {
	if strings.TrimSpace(publicationID) == "" || lease.Owner == "" || lease.Token == "" || ttl <= 0 {
		return platforminsights.ErrLeaseLost
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE analytics_sync_jobs SET lease_until=?,updated_at=? WHERE publication_id=? AND lease_owner=? AND lease_token=? AND lease_until>?`, stamp(now.Add(ttl)), stamp(now), publicationID, lease.Owner, lease.Token, stamp(now))
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return platforminsights.ErrLeaseLost
	}
	return nil
}

func (s *Store) AppendEvent(ctx context.Context, publicationID, eventType string, payload map[string]any) error {
	return s.appendEvent(ctx, publicationID, eventType, payload)
}

// AppendEventFenced appends an event only while the caller owns a live lease.
func (s *Store) AppendEventFenced(ctx context.Context, publicationID string, lease Lease, eventType string, payload map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assertLeaseTx(ctx, tx, publicationID, lease, time.Now().UTC()); err != nil {
		return err
	}
	if err := appendEventTx(ctx, tx, publicationID, eventType, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) appendEvent(ctx context.Context, publicationID, eventType string, payload map[string]any) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO analytics_events(publication_id,type,payload_json,created_at) VALUES(?,?,?,?)`, publicationID, eventType, nullableJSON(payload), stamp(time.Now()))
	return err
}

func appendEventTx(ctx context.Context, tx *sql.Tx, publicationID, eventType string, payload map[string]any) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO analytics_events(publication_id,type,payload_json,created_at) VALUES(?,?,?,?)`, publicationID, eventType, nullableJSON(payload), stamp(time.Now()))
	return err
}

func (s *Store) SaveReviewSnapshot(ctx context.Context, snapshot ReviewSnapshot) (ReviewSnapshot, bool, error) {
	return s.saveReviewSnapshot(ctx, snapshot, nil)
}

// SaveReviewSnapshotFenced appends a review observation only while the caller
// owns a live publication sync lease.
func (s *Store) SaveReviewSnapshotFenced(ctx context.Context, snapshot ReviewSnapshot, lease Lease) (ReviewSnapshot, bool, error) {
	return s.saveReviewSnapshot(ctx, snapshot, &lease)
}

func (s *Store) saveReviewSnapshot(ctx context.Context, snapshot ReviewSnapshot, lease *Lease) (ReviewSnapshot, bool, error) {
	if snapshot.ID == "" {
		snapshot.ID = "review_" + uuid.NewString()
	}
	if snapshot.ObservationHash == "" {
		snapshot.ObservationHash = hashValue(struct {
			Provider, Platform, Status, ExternalID, ExternalURL, Proof string
			Verified                                                   bool
		}{snapshot.Provider, snapshot.Platform, string(snapshot.Status), snapshot.ExternalID, snapshot.ExternalURL, snapshot.Proof, snapshot.Verified})
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = time.Now().UTC()
	}
	if snapshot.LastSeenAt.IsZero() {
		snapshot.LastSeenAt = snapshot.ObservedAt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReviewSnapshot{}, false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if lease != nil {
		if err := assertLeaseTx(ctx, tx, snapshot.PublicationID, *lease, now); err != nil {
			return ReviewSnapshot{}, false, err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO analytics_review_snapshots(id,publication_id,provider,platform,status,external_id,external_url,verified,proof,observation_hash,observed_at,last_seen_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(publication_id,provider,observation_hash) DO NOTHING`, snapshot.ID, snapshot.PublicationID, snapshot.Provider, snapshot.Platform, string(snapshot.Status), snapshot.ExternalID, snapshot.ExternalURL, boolInt(snapshot.Verified), snapshot.Proof, snapshot.ObservationHash, stamp(snapshot.ObservedAt), stamp(snapshot.LastSeenAt))
	if err != nil {
		return ReviewSnapshot{}, false, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE analytics_review_snapshots SET last_seen_at=? WHERE publication_id=? AND provider=? AND observation_hash=?`, stamp(snapshot.LastSeenAt), snapshot.PublicationID, snapshot.Provider, snapshot.ObservationHash); err != nil {
			return ReviewSnapshot{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ReviewSnapshot{}, false, err
	}
	return snapshot, count == 1, nil
}

func (s *Store) SaveMetricSnapshot(ctx context.Context, snapshot MetricSnapshot) (MetricSnapshot, bool, error) {
	return s.saveMetricSnapshot(ctx, snapshot, nil)
}

// SaveMetricSnapshotFenced appends a metric observation only while the caller
// owns a live publication sync lease.
func (s *Store) SaveMetricSnapshotFenced(ctx context.Context, snapshot MetricSnapshot, lease Lease) (MetricSnapshot, bool, error) {
	return s.saveMetricSnapshot(ctx, snapshot, &lease)
}

func (s *Store) saveMetricSnapshot(ctx context.Context, snapshot MetricSnapshot, lease *Lease) (MetricSnapshot, bool, error) {
	if snapshot.ID == "" {
		snapshot.ID = "metric_" + uuid.NewString()
	}
	if snapshot.ObservationHash == "" {
		snapshot.ObservationHash = hashValue(struct {
			Provider, Platform, ExternalID string
			Metrics                        map[string]float64
			Raw                            map[string]any
		}{snapshot.Provider, snapshot.Platform, snapshot.ExternalID, snapshot.Metrics, snapshot.Raw})
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = time.Now().UTC()
	}
	if snapshot.LastSeenAt.IsZero() {
		snapshot.LastSeenAt = snapshot.ObservedAt
	}
	metrics, err := json.Marshal(snapshot.Metrics)
	if err != nil {
		return MetricSnapshot{}, false, err
	}
	raw, err := optionalJSON(snapshot.Raw)
	if err != nil {
		return MetricSnapshot{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MetricSnapshot{}, false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if lease != nil {
		if err := assertLeaseTx(ctx, tx, snapshot.PublicationID, *lease, now); err != nil {
			return MetricSnapshot{}, false, err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO analytics_metric_snapshots(id,publication_id,provider,platform,external_id,observation_hash,metrics_json,raw_json,observed_at,last_seen_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(publication_id,provider,observation_hash) DO NOTHING`, snapshot.ID, snapshot.PublicationID, snapshot.Provider, snapshot.Platform, snapshot.ExternalID, snapshot.ObservationHash, string(metrics), raw, stamp(snapshot.ObservedAt), stamp(snapshot.LastSeenAt))
	if err != nil {
		return MetricSnapshot{}, false, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE analytics_metric_snapshots SET last_seen_at=? WHERE publication_id=? AND provider=? AND observation_hash=?`, stamp(snapshot.LastSeenAt), snapshot.PublicationID, snapshot.Provider, snapshot.ObservationHash); err != nil {
			return MetricSnapshot{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MetricSnapshot{}, false, err
	}
	return snapshot, count == 1, nil
}

func assertLeaseTx(ctx context.Context, tx *sql.Tx, publicationID string, lease Lease, now time.Time) error {
	if lease.Owner == "" || lease.Token == "" {
		return platforminsights.ErrLeaseLost
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM analytics_sync_jobs WHERE publication_id=? AND lease_owner=? AND lease_token=? AND lease_until>?`, publicationID, lease.Owner, lease.Token, stamp(now)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return platforminsights.ErrLeaseLost
	}
	return err
}

func (s *Store) CountReviewSnapshots(ctx context.Context, publicationID string) (int, error) {
	return s.count(ctx, `SELECT count(*) FROM analytics_review_snapshots WHERE publication_id=?`, publicationID)
}
func (s *Store) CountMetricSnapshots(ctx context.Context, publicationID string) (int, error) {
	return s.count(ctx, `SELECT count(*) FROM analytics_metric_snapshots WHERE publication_id=?`, publicationID)
}
func (s *Store) CountEvents(ctx context.Context, publicationID string) (int, error) {
	return s.count(ctx, `SELECT count(*) FROM analytics_events WHERE publication_id=?`, publicationID)
}

func (s *Store) Summary(ctx context.Context) (Summary, error) {
	var summary Summary
	summary.JobsByStatus = make(map[string]int)
	queries := []struct {
		destination *int
		query       string
	}{
		{&summary.ReviewSnapshots, `SELECT count(*) FROM analytics_review_snapshots`},
		{&summary.MetricSnapshots, `SELECT count(*) FROM analytics_metric_snapshots`},
		{&summary.Events, `SELECT count(*) FROM analytics_events`},
		{&summary.PublicationCount, `SELECT count(*) FROM (SELECT publication_id FROM analytics_sync_jobs UNION SELECT publication_id FROM analytics_review_snapshots UNION SELECT publication_id FROM analytics_metric_snapshots)`},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query).Scan(item.destination); err != nil {
			return Summary{}, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT status,count(*) FROM analytics_sync_jobs GROUP BY status`)
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return Summary{}, err
		}
		summary.JobsByStatus[status] = count
	}
	if err := rows.Err(); err != nil {
		return Summary{}, err
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT max(observed_at) FROM (SELECT observed_at FROM analytics_review_snapshots UNION ALL SELECT observed_at FROM analytics_metric_snapshots)`).Scan(&last); err != nil {
		return Summary{}, err
	}
	if last.Valid && last.String != "" {
		parsed, err := time.Parse(time.RFC3339Nano, last.String)
		if err != nil {
			return Summary{}, err
		}
		summary.LastObservedAt = &parsed
	}
	return summary, nil
}

func (s *Store) ListReviewSnapshots(ctx context.Context, publicationID string) ([]ReviewSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,publication_id,provider,platform,status,external_id,external_url,verified,proof,observation_hash,observed_at,last_seen_at FROM analytics_review_snapshots WHERE publication_id=? ORDER BY observed_at,rowid`, publicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []ReviewSnapshot
	for rows.Next() {
		var snapshot ReviewSnapshot
		var status, observedAt, lastSeenAt string
		var verified int
		if err := rows.Scan(&snapshot.ID, &snapshot.PublicationID, &snapshot.Provider, &snapshot.Platform, &status, &snapshot.ExternalID, &snapshot.ExternalURL, &verified, &snapshot.Proof, &snapshot.ObservationHash, &observedAt, &lastSeenAt); err != nil {
			return nil, err
		}
		snapshot.Status = platforminsights.PublicationStatus(status)
		snapshot.Verified = verified != 0
		snapshot.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, err
		}
		snapshot.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenAt)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Store) ListMetricSnapshots(ctx context.Context, publicationID string) ([]MetricSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,publication_id,provider,platform,external_id,observation_hash,metrics_json,raw_json,observed_at,last_seen_at FROM analytics_metric_snapshots WHERE publication_id=? ORDER BY observed_at,rowid`, publicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []MetricSnapshot
	for rows.Next() {
		var snapshot MetricSnapshot
		var metricsJSON, observedAt, lastSeenAt string
		var raw sql.NullString
		if err := rows.Scan(&snapshot.ID, &snapshot.PublicationID, &snapshot.Provider, &snapshot.Platform, &snapshot.ExternalID, &snapshot.ObservationHash, &metricsJSON, &raw, &observedAt, &lastSeenAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metricsJSON), &snapshot.Metrics); err != nil {
			return nil, err
		}
		if raw.Valid && raw.String != "" {
			if err := json.Unmarshal([]byte(raw.String), &snapshot.Raw); err != nil {
				return nil, err
			}
		}
		snapshot.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, err
		}
		snapshot.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenAt)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Store) count(ctx context.Context, query, id string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, query, id).Scan(&count)
	return count, err
}

func (s *Store) ListEvents(ctx context.Context, publicationID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,publication_id,type,payload_json,created_at FROM analytics_events WHERE publication_id=? ORDER BY id`, publicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var payload, created sql.NullString
		if err := rows.Scan(&event.ID, &event.PublicationID, &event.Type, &payload, &created); err != nil {
			return nil, err
		}
		event.Payload = map[string]any{}
		if payload.Valid && payload.String != "" {
			if err := json.Unmarshal([]byte(payload.String), &event.Payload); err != nil {
				return nil, err
			}
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created.String)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func scanJob(row interface{ Scan(...any) error }) (SyncJob, error) {
	var job SyncJob
	var next, until, created, updated string
	err := row.Scan(&job.PublicationID, &job.Status, &job.Attempts, &next, &job.LastError, &job.LeaseOwner, &job.LeaseToken, &until, &created, &updated)
	if err != nil {
		return SyncJob{}, err
	}
	for _, field := range []struct {
		value  string
		target *time.Time
	}{{next, &job.NextAttemptAt}, {until, &job.LeaseUntil}, {created, &job.CreatedAt}, {updated, &job.UpdatedAt}} {
		if field.value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, field.value)
			if err != nil {
				return SyncJob{}, err
			}
			*field.target = parsed
		}
	}
	return job, nil
}

func hashValue(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func nullableJSON(value any) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return string(encoded)
}
func optionalJSON(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}
