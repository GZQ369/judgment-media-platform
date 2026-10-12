package runstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var (
	ErrRunNotFound         = errors.New("run not found")
	ErrLeaseBusy           = errors.New("run lease is held by another worker")
	ErrLeaseLost           = errors.New("run lease was lost")
	ErrIdempotencyConflict = errors.New("idempotency key conflicts with an existing request")
)

// Store is the durable SQLite ledger for asynchronous distribution runs.
type Store struct {
	db *sql.DB
}

// Lease identifies one fenced ownership epoch for a run.
type Lease struct {
	Owner string
	Token string
}

// Open creates or opens a WAL-backed SQLite run ledger.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("runstore path is required")
	}
	db, err := sql.Open("sqlite", filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open runstore: %w", err)
	}
	// SQLite connection pragmas are per connection. A single pooled connection
	// keeps these settings true for every operation while still using WAL.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  idempotency_key TEXT UNIQUE,
  status TEXT NOT NULL,
  request_json TEXT NOT NULL,
  checkpoint TEXT NOT NULL DEFAULT '',
  capability_block_json TEXT,
  error_json TEXT,
  attempt INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT
);
CREATE TABLE IF NOT EXISTS run_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  checkpoint TEXT NOT NULL DEFAULT '',
  payload_json TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS run_events_run_id_id ON run_events(run_id, id);
CREATE TABLE IF NOT EXISTS run_artifacts (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  path TEXT,
  sha256 TEXT,
  metadata_json TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS run_artifacts_run_id ON run_artifacts(run_id, created_at);
CREATE TABLE IF NOT EXISTS run_leases (
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  owner TEXT NOT NULL,
  token TEXT NOT NULL DEFAULT '',
  lease_until TEXT NOT NULL,
  lease_until_ns INTEGER,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS distribution_retry_receipts (
  operation_key TEXT PRIMARY KEY,
  parent_id TEXT NOT NULL,
  parent_attempt INTEGER NOT NULL CHECK(parent_attempt > 0),
  child_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  child_attempt INTEGER NOT NULL,
  outcome TEXT NOT NULL CHECK(outcome IN ('queued','noop')),
  created_at TEXT NOT NULL,
  UNIQUE(parent_id,parent_attempt,child_id)
);
`)
	if err != nil {
		return fmt.Errorf("migrate runstore: %w", err)
	}
	if err := ensureColumn(s.db, "run_leases", "token", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate runstore lease token: %w", err)
	}
	if err := ensureColumn(s.db, "run_leases", "lease_until_ns", "INTEGER"); err != nil {
		return fmt.Errorf("migrate runstore lease timestamp: %w", err)
	}
	if err := migrateLeaseTimestamps(s.db, "run_leases"); err != nil {
		return fmt.Errorf("migrate runstore lease timestamps: %w", err)
	}
	return nil
}

// migrateLeaseTimestamps backfills the numeric comparison value before any
// worker can inspect the database. Invalid legacy data stops startup rather
// than treating an existing lease as expired and reclaiming it.
func migrateLeaseTimestamps(db *sql.DB, table string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT run_id,lease_until FROM ` + table + ` WHERE lease_until_ns IS NULL OR lease_until_ns=0`)
	if err != nil {
		return err
	}
	type legacyLease struct {
		id string
		at string
	}
	var pending []legacyLease
	for rows.Next() {
		var item legacyLease
		if err := rows.Scan(&item.id, &item.at); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range pending {
		at, err := parseLeaseTimestamp(item.at)
		if err != nil {
			return fmt.Errorf("run %q has invalid lease_until %q: %w", item.id, item.at, err)
		}
		if _, err := tx.Exec(`UPDATE `+table+` SET lease_until_ns=? WHERE run_id=?`, at, item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func parseLeaseTimestamp(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, err
	}
	return parsed.UTC().UnixNano(), nil
}

func leaseTimestamp(t time.Time) (string, int64) {
	t = t.UTC()
	return t.Format(time.RFC3339Nano), t.UnixNano()
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error { return s.db.Close() }

// CreateRun persists a queued run and its creation event. Existing idempotency keys return the original run.
func (s *Store) CreateRun(ctx context.Context, request domain.DistributionRequest) (domain.Run, bool, error) {
	if request.Atom != nil {
		if err := request.Atom.Validate(); err != nil {
			return domain.Run{}, false, fmt.Errorf("validate content atom: %w", err)
		}
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return domain.Run{}, false, fmt.Errorf("marshal run request: %w", err)
	}
	now := time.Now().UTC()
	id := "run_" + uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, false, err
	}
	defer tx.Rollback()
	if request.IdempotencyKey != "" {
		var existing, existingJSON string
		err = tx.QueryRowContext(ctx, `SELECT id,request_json FROM runs WHERE idempotency_key = ?`, request.IdempotencyKey).Scan(&existing, &existingJSON)
		if err == nil {
			if existingJSON != string(requestJSON) {
				return domain.Run{}, false, ErrIdempotencyConflict
			}
			if err := tx.Commit(); err != nil {
				return domain.Run{}, false, err
			}
			run, err := s.GetRun(ctx, existing)
			return run, false, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.Run{}, false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs (id,idempotency_key,status,request_json,created_at,updated_at) VALUES (?,?,?,?,?,?)`, id, nullable(request.IdempotencyKey), domain.RunStatusQueued, requestJSON, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		if request.IdempotencyKey != "" {
			_ = tx.Rollback()
			var existingID, existingJSON string
			lookupErr := s.db.QueryRowContext(ctx, `SELECT id,request_json FROM runs WHERE idempotency_key = ?`, request.IdempotencyKey).Scan(&existingID, &existingJSON)
			if lookupErr == nil {
				if existingJSON != string(requestJSON) {
					return domain.Run{}, false, ErrIdempotencyConflict
				}
				stored, getErr := s.GetRun(ctx, existingID)
				return stored, false, getErr
			}
		}
		return domain.Run{}, false, fmt.Errorf("insert run: %w", err)
	}
	if err := insertEventTx(ctx, tx, id, "run_queued", "queued", map[string]any{"idempotency_key": request.IdempotencyKey}); err != nil {
		return domain.Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, false, err
	}
	run, err := s.GetRun(ctx, id)
	return run, true, err
}

// GetRun returns a run with its durable artifacts.
func (s *Store) GetRun(ctx context.Context, id string) (domain.Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,status,request_json,checkpoint,capability_block_json,error_json,attempt,created_at,updated_at,started_at,finished_at FROM runs WHERE id = ?`, id)
	return s.scanRun(ctx, row)
}

// ListRuns returns newest runs first.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]domain.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,status,request_json,checkpoint,capability_block_json,error_json,attempt,created_at,updated_at,started_at,finished_at FROM runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var raw []storedRun
	for rows.Next() {
		item, err := scanStoredRun(rows)
		if err != nil {
			return nil, err
		}
		raw = append(raw, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.Run, 0, len(raw))
	for _, item := range raw {
		run, err := s.hydrateRun(ctx, item)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// RecoverRunning requeues runs left running by a previous process, but keeps
// a run owned by a live worker lease untouched.
func (s *Store) RecoverRunning(ctx context.Context) error {
	now, nowNS := leaseTimestamp(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,updated_at=?,checkpoint=? WHERE status=? AND NOT EXISTS (SELECT 1 FROM run_leases l WHERE l.run_id=runs.id AND l.lease_until_ns>?)`, domain.RunStatusQueued, now, "recovered", domain.RunStatusRunning, nowNS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_leases WHERE lease_until_ns<=?`, nowNS); err != nil {
		return err
	}
	return tx.Commit()
}

// RunnableIDs returns queued run ids in creation order.
func (s *Store) RunnableIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM runs WHERE status = ? ORDER BY created_at`, domain.RunStatusQueued)
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
	return ids, rows.Err()
}

// UpdateRun persists a state transition and appends its event atomically.
func (s *Store) UpdateRun(ctx context.Context, id string, status domain.RunStatus, checkpoint string, block *domain.CapabilityBlock, runErr *domain.RunError, payload map[string]any) error {
	return s.updateRun(ctx, id, status, checkpoint, block, runErr, payload, nil)
}

// UpdateRunFenced persists a transition only for the current lease epoch.
func (s *Store) UpdateRunFenced(ctx context.Context, id string, lease Lease, status domain.RunStatus, checkpoint string, block *domain.CapabilityBlock, runErr *domain.RunError, payload map[string]any) error {
	return s.updateRun(ctx, id, status, checkpoint, block, runErr, payload, &lease)
}

func (s *Store) updateRun(ctx context.Context, id string, status domain.RunStatus, checkpoint string, block *domain.CapabilityBlock, runErr *domain.RunError, payload map[string]any, lease *Lease) error {
	blockJSON, err := optionalJSON(block)
	if err != nil {
		return err
	}
	errorJSON, err := optionalJSON(runErr)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	started := any(nil)
	finished := any(nil)
	if status == domain.RunStatusRunning {
		started = now.Format(time.RFC3339Nano)
	}
	if status == domain.RunStatusBlocked || status == domain.RunStatusPackageReady || status == domain.RunStatusMediaReady || status == domain.RunStatusFailed || status == domain.RunStatusCancelled {
		finished = now.Format(time.RFC3339Nano)
	}
	query := `UPDATE runs SET status=?,checkpoint=?,capability_block_json=?,error_json=?,updated_at=?,started_at=COALESCE(started_at,?),finished_at=? WHERE id=?`
	args := []any{status, checkpoint, blockJSON, errorJSON, now.Format(time.RFC3339Nano), started, finished, id}
	if lease != nil {
		_, nowNS := leaseTimestamp(now)
		query += ` AND EXISTS (SELECT 1 FROM run_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?)`
		args = append(args, id, lease.Owner, lease.Token, nowNS)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		if lease != nil {
			return ErrLeaseLost
		}
		return ErrRunNotFound
	}
	if err := insertEventTx(ctx, tx, id, string(status), checkpoint, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// SetAttempt increments the retry attempt and records a checkpoint event atomically.
func (s *Store) SetAttempt(ctx context.Context, id string, attempt int, checkpoint string, eventType string, payload map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE runs SET attempt=?,updated_at=?,checkpoint=? WHERE id=?`, attempt, time.Now().UTC().Format(time.RFC3339Nano), checkpoint, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrRunNotFound
	}
	if err := insertEventTx(ctx, tx, id, eventType, checkpoint, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// AppendEvent appends an event to the ledger.
func (s *Store) AppendEvent(ctx context.Context, id, eventType, checkpoint string, payload map[string]any) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO run_events (run_id,type,checkpoint,payload_json,created_at) VALUES (?,?,?,?,?)`, id, eventType, checkpoint, nullableJSON(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// AppendEventFenced appends an event only while the lease epoch is current.
func (s *Store) AppendEventFenced(ctx context.Context, id string, lease Lease, eventType, checkpoint string, payload map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var one int
	_, nowNS := leaseTimestamp(time.Now())
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM run_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?`, id, lease.Owner, lease.Token, nowNS).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	if err := insertEventTx(ctx, tx, id, eventType, checkpoint, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// ListEvents returns events after the supplied event id.
func (s *Store) ListEvents(ctx context.Context, runID string, afterID int64, limit int) ([]domain.RunEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,type,checkpoint,payload_json,created_at FROM run_events WHERE run_id=? AND id>? ORDER BY id LIMIT ?`, runID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.RunEvent
	for rows.Next() {
		var event domain.RunEvent
		var payload, created sql.NullString
		if err := rows.Scan(&event.ID, &event.RunID, &event.Type, &event.Checkpoint, &payload, &created); err != nil {
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

// AddArtifact records an artifact and emits its ledger event atomically.
func (s *Store) AddArtifact(ctx context.Context, artifact domain.Artifact) error {
	return s.addArtifact(ctx, artifact, nil)
}

// AddArtifactFenced records an artifact only while the lease epoch is current.
func (s *Store) AddArtifactFenced(ctx context.Context, artifact domain.Artifact, lease Lease) error {
	return s.addArtifact(ctx, artifact, &lease)
}

func (s *Store) addArtifact(ctx context.Context, artifact domain.Artifact, lease *Lease) error {
	metadata, err := optionalJSON(artifact.Metadata)
	if err != nil {
		return err
	}
	if artifact.ID == "" {
		artifact.ID = "artifact_" + uuid.NewString()
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if lease != nil {
		var one int
		_, nowNS := leaseTimestamp(time.Now())
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM run_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?`, artifact.RunID, lease.Owner, lease.Token, nowNS).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrLeaseLost
			}
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO run_artifacts (id,run_id,kind,path,sha256,metadata_json,created_at) VALUES (?,?,?,?,?,?,?)`, artifact.ID, artifact.RunID, artifact.Kind, artifact.Path, artifact.SHA256, metadata, artifact.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := insertEventTx(ctx, tx, artifact.RunID, "artifact_recorded", "artifact_recorded", map[string]any{"artifact_id": artifact.ID, "kind": artifact.Kind, "path": artifact.Path}); err != nil {
		return err
	}
	return tx.Commit()
}

// AcquireLease claims a run lease and returns a fresh fencing token.
func (s *Store) AcquireLease(ctx context.Context, runID, owner string, ttl time.Duration) (Lease, error) {
	if owner == "" || ttl <= 0 {
		return Lease{}, ErrLeaseBusy
	}
	now := time.Now().UTC()
	until := now.Add(ttl)
	nowStamp, nowNS := leaseTimestamp(now)
	untilStamp, untilNS := leaseTimestamp(until)
	lease := Lease{Owner: owner, Token: uuid.NewString()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO run_leases (run_id,owner,token,lease_until,lease_until_ns,updated_at) VALUES (?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET owner=excluded.owner,token=excluded.token,lease_until=excluded.lease_until,lease_until_ns=excluded.lease_until_ns,updated_at=excluded.updated_at WHERE run_leases.lease_until_ns<=?`, runID, lease.Owner, lease.Token, untilStamp, untilNS, nowStamp, nowNS)
	if err != nil {
		return Lease{}, err
	}
	var current Lease
	if err := tx.QueryRowContext(ctx, `SELECT owner,token FROM run_leases WHERE run_id=?`, runID).Scan(&current.Owner, &current.Token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Lease{}, ErrLeaseBusy
		}
		return Lease{}, err
	}
	if current != lease {
		return Lease{}, ErrLeaseBusy
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

// TryLease retains the original lease API for callers that do not need fencing.
func (s *Store) TryLease(ctx context.Context, runID, owner string, ttl time.Duration) error {
	_, err := s.AcquireLease(ctx, runID, owner, ttl)
	return err
}

// RenewLease extends a lease only for the same owner and fencing token.
func (s *Store) RenewLease(ctx context.Context, runID string, lease Lease, ttl time.Duration) error {
	now := time.Now().UTC()
	stamp, nowNS := leaseTimestamp(now)
	untilStamp, untilNS := leaseTimestamp(now.Add(ttl))
	result, err := s.db.ExecContext(ctx, `UPDATE run_leases SET lease_until=?,lease_until_ns=?,updated_at=? WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?`, untilStamp, untilNS, stamp, runID, lease.Owner, lease.Token, nowNS)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}

// ReleaseLease releases a lease only when owned by owner.
func (s *Store) ReleaseLease(ctx context.Context, runID, owner string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_leases WHERE run_id=? AND owner=?`, runID, owner)
	return err
}

// ReleaseLeaseToken prevents an old worker from releasing a newer lease epoch.
func (s *Store) ReleaseLeaseToken(ctx context.Context, runID string, lease Lease) error {
	_, nowNS := leaseTimestamp(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM run_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?`, runID, lease.Owner, lease.Token, nowNS)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 1 {
		return tx.Commit()
	}
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM run_leases WHERE run_id=? AND owner=? AND token=?`, runID, lease.Owner, lease.Token).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return err
	}
	return ErrLeaseLost
}

// CancelRun atomically marks a non-terminal run cancelled and revokes its lease.
func (s *Store) CancelRun(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,checkpoint=?,updated_at=?,finished_at=? WHERE id=? AND status IN (?,?)`, domain.RunStatusCancelled, "cancelled", now, now, id, domain.RunStatusQueued, domain.RunStatusRunning)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRunNotFound
			}
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_leases WHERE run_id=?`, id); err != nil {
		return err
	}
	if err := insertEventTx(ctx, tx, id, "cancelled", "cancelled", map[string]any{"reason": "api_cancel"}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) scanRun(ctx context.Context, row interface{ Scan(...any) error }) (domain.Run, error) {
	item, err := scanStoredRun(row)
	if err != nil {
		return domain.Run{}, err
	}
	return s.hydrateRun(ctx, item)
}

type storedRun struct {
	id, status, requestJSON, checkpoint, blockJSON, errorJSON, created, updated string
	attempt                                                                     int
	started, finished                                                           sql.NullString
}

func scanStoredRun(row interface{ Scan(...any) error }) (storedRun, error) {
	var status, requestJSON, created, updated string
	var checkpoint sql.NullString
	var blockJSON, errorJSON sql.NullString
	var attempt int
	var started, finished sql.NullString
	var id string
	if err := row.Scan(&id, &status, &requestJSON, &checkpoint, &blockJSON, &errorJSON, &attempt, &created, &updated, &started, &finished); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storedRun{}, ErrRunNotFound
		}
		return storedRun{}, err
	}
	return storedRun{id: id, status: status, requestJSON: requestJSON, checkpoint: checkpoint.String, blockJSON: blockJSON.String, errorJSON: errorJSON.String, attempt: attempt, created: created, updated: updated, started: started, finished: finished}, nil
}

func (s *Store) hydrateRun(ctx context.Context, item storedRun) (domain.Run, error) {
	var run domain.Run
	if err := json.Unmarshal([]byte(item.requestJSON), &run.Request); err != nil {
		return domain.Run{}, err
	}
	run.ID, run.Status, run.Checkpoint, run.Attempt = item.id, domain.RunStatus(item.status), item.checkpoint, item.attempt
	var err error
	run.CreatedAt, err = time.Parse(time.RFC3339Nano, item.created)
	if err != nil {
		return domain.Run{}, err
	}
	run.UpdatedAt, err = time.Parse(time.RFC3339Nano, item.updated)
	if err != nil {
		return domain.Run{}, err
	}
	if item.started.Valid {
		value, e := time.Parse(time.RFC3339Nano, item.started.String)
		if e != nil {
			return domain.Run{}, e
		}
		run.StartedAt = &value
	}
	if item.finished.Valid {
		value, e := time.Parse(time.RFC3339Nano, item.finished.String)
		if e != nil {
			return domain.Run{}, e
		}
		run.FinishedAt = &value
	}
	if item.blockJSON != "" {
		run.CapabilityBlock = &domain.CapabilityBlock{}
		if err := json.Unmarshal([]byte(item.blockJSON), run.CapabilityBlock); err != nil {
			return domain.Run{}, err
		}
	}
	if item.errorJSON != "" {
		run.Error = &domain.RunError{}
		if err := json.Unmarshal([]byte(item.errorJSON), run.Error); err != nil {
			return domain.Run{}, err
		}
	}
	run.Artifacts, err = s.artifacts(ctx, run.ID)
	return run, err
}

func (s *Store) artifacts(ctx context.Context, runID string) ([]domain.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,kind,path,sha256,metadata_json,created_at FROM run_artifacts WHERE run_id=? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Artifact
	for rows.Next() {
		var artifact domain.Artifact
		var path, sha256, metadata, created sql.NullString
		if err := rows.Scan(&artifact.ID, &artifact.RunID, &artifact.Kind, &path, &sha256, &metadata, &created); err != nil {
			return nil, err
		}
		artifact.Path, artifact.SHA256 = path.String, sha256.String
		artifact.Metadata = map[string]any{}
		if metadata.Valid && metadata.String != "" {
			if err := json.Unmarshal([]byte(metadata.String), &artifact.Metadata); err != nil {
				return nil, err
			}
		}
		var err error
		artifact.CreatedAt, err = time.Parse(time.RFC3339Nano, created.String)
		if err != nil {
			return nil, err
		}
		out = append(out, artifact)
	}
	return out, rows.Err()
}

func insertEventTx(ctx context.Context, tx *sql.Tx, runID, eventType, checkpoint string, payload map[string]any) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_events (run_id,type,checkpoint,payload_json,created_at) VALUES (?,?,?,?,?)`, runID, eventType, checkpoint, nullableJSON(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func optionalJSON(value any) (any, error) {
	if value == nil || (reflect.ValueOf(value).Kind() == reflect.Ptr && reflect.ValueOf(value).IsNil()) {
		return nil, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func nullableJSON(value any) any {
	if value == nil {
		return nil
	}
	b, _ := json.Marshal(value)
	return string(b)
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}
