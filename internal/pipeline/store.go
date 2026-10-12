package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/orchestration"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var (
	// ErrRunNotFound indicates that a pipeline id is unknown.
	ErrRunNotFound = errors.New("pipeline run not found")
	// ErrLeaseBusy indicates that another worker currently owns a run.
	ErrLeaseBusy = errors.New("pipeline run lease is held by another worker")
	// ErrLeaseLost indicates that a fenced write came from an old worker epoch.
	ErrLeaseLost = errors.New("pipeline run lease was lost")
	// ErrIdempotencyConflict indicates that a key was reused with another request.
	ErrIdempotencyConflict = errors.New("pipeline idempotency key conflicts with an existing request")
)

// Store is an independent SQLite ledger for pipeline runs. Its table names and
// connection are intentionally separate from internal/runstore.
type Store struct{ db *sql.DB }

// Lease identifies one fenced pipeline worker epoch.
type Lease struct {
	Owner string
	Token string
}

// NewStore opens the configured pipeline ledger.
func NewStore(config Config) (*Store, error) { return Open(config.Path) }

// Open creates or opens a WAL-backed pipeline ledger.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("pipeline store path is required")
	}
	db, err := sql.Open("sqlite", filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open pipeline store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure pipeline sqlite: %w", err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS pipeline_runs (
  id TEXT PRIMARY KEY,
  idempotency_key TEXT UNIQUE,
  status TEXT NOT NULL,
  request_json TEXT NOT NULL,
  research_json TEXT,
  judgment_json TEXT,
  thesis_json TEXT,
  atoms_json TEXT,
  children_json TEXT,
  checkpoint TEXT NOT NULL DEFAULT '',
  capability_block_json TEXT,
  error_json TEXT,
  attempt INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT
);
CREATE TABLE IF NOT EXISTS pipeline_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  checkpoint TEXT NOT NULL DEFAULT '',
  payload_json TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS pipeline_events_run_id_id ON pipeline_events(run_id, id);
CREATE TABLE IF NOT EXISTS pipeline_leases (
  run_id TEXT PRIMARY KEY REFERENCES pipeline_runs(id) ON DELETE CASCADE,
  owner TEXT NOT NULL,
  token TEXT NOT NULL,
  lease_until TEXT NOT NULL,
  lease_until_ns INTEGER,
  updated_at TEXT NOT NULL
);`)
	if err != nil {
		return fmt.Errorf("migrate pipeline store: %w", err)
	}
	if err := ensureColumn(s.db, "pipeline_leases", "lease_until_ns", "INTEGER"); err != nil {
		return fmt.Errorf("migrate pipeline lease timestamp: %w", err)
	}
	if err := migrateLeaseTimestamps(s.db, "pipeline_leases"); err != nil {
		return fmt.Errorf("migrate pipeline lease timestamps: %w", err)
	}
	return nil
}

// migrateLeaseTimestamps backfills old RFC3339Nano lease rows before workers
// can inspect them. Invalid legacy data stops startup instead of reclaiming a
// lease that may still be active.
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
			return fmt.Errorf("pipeline %q has invalid lease_until %q: %w", item.id, item.at, err)
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

// Close closes the pipeline SQLite connection.
func (s *Store) Close() error { return s.db.Close() }

// CreateRun persists a queued pipeline and its initial append-only event.
func (s *Store) CreateRun(ctx context.Context, request PipelineRequest) (PipelineRun, bool, error) {
	if err := request.Validate(); err != nil {
		return PipelineRun{}, false, err
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return PipelineRun{}, false, fmt.Errorf("marshal pipeline request: %w", err)
	}
	now := time.Now().UTC()
	id := "pipeline_" + uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PipelineRun{}, false, err
	}
	defer tx.Rollback()
	if request.IdempotencyKey != "" {
		var existing, existingJSON string
		err = tx.QueryRowContext(ctx, `SELECT id,request_json FROM pipeline_runs WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&existing, &existingJSON)
		if err == nil {
			if existingJSON != string(requestJSON) {
				return PipelineRun{}, false, ErrIdempotencyConflict
			}
			if err := tx.Commit(); err != nil {
				return PipelineRun{}, false, err
			}
			run, err := s.GetRun(ctx, existing)
			return run, false, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return PipelineRun{}, false, err
		}
	}
	stamp := now.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_runs (id,idempotency_key,status,request_json,created_at,updated_at) VALUES (?,?,?,?,?,?)`, id, nullable(request.IdempotencyKey), PipelineStatusQueued, requestJSON, stamp, stamp)
	if err != nil {
		if request.IdempotencyKey != "" {
			_ = tx.Rollback()
			var existingID, existingJSON string
			if lookupErr := s.db.QueryRowContext(ctx, `SELECT id,request_json FROM pipeline_runs WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&existingID, &existingJSON); lookupErr == nil {
				if existingJSON != string(requestJSON) {
					return PipelineRun{}, false, ErrIdempotencyConflict
				}
				run, getErr := s.GetRun(ctx, existingID)
				return run, false, getErr
			}
		}
		return PipelineRun{}, false, fmt.Errorf("insert pipeline run: %w", err)
	}
	if err := insertEventTx(ctx, tx, id, "run_queued", string(PipelineStatusQueued), map[string]any{"idempotency_key": request.IdempotencyKey}); err != nil {
		return PipelineRun{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return PipelineRun{}, false, err
	}
	run, err := s.GetRun(ctx, id)
	return run, true, err
}

// GetRun returns a durable pipeline view.
func (s *Store) GetRun(ctx context.Context, id string) (PipelineRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,status,request_json,research_json,judgment_json,thesis_json,atoms_json,children_json,checkpoint,capability_block_json,error_json,attempt,created_at,updated_at,started_at,finished_at FROM pipeline_runs WHERE id=?`, id)
	return s.scanRun(row)
}

// ListRuns returns newest pipeline runs first.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]PipelineRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,status,request_json,research_json,judgment_json,thesis_json,atoms_json,children_json,checkpoint,capability_block_json,error_json,attempt,created_at,updated_at,started_at,finished_at FROM pipeline_runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PipelineRun
	for rows.Next() {
		run, err := s.scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// RunnableIDs returns queued pipeline ids in creation order.
func (s *Store) RunnableIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM pipeline_runs WHERE status=? ORDER BY created_at`, PipelineStatusQueued)
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

// RecoverRunning requeues runs whose previous lease expired.
func (s *Store) RecoverRunning(ctx context.Context) error {
	now, nowNS := leaseTimestamp(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET status=?,updated_at=?,checkpoint=? WHERE status=? AND NOT EXISTS (SELECT 1 FROM pipeline_leases l WHERE l.run_id=pipeline_runs.id AND l.lease_until_ns>?)`, PipelineStatusQueued, now, "recovered", PipelineStatusRunning, nowNS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_leases WHERE lease_until_ns<=?`, nowNS); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveWorkflowFenced persists the research, judgment, thesis, and atoms from
// one successful judgment workflow while holding the current lease.
func (s *Store) SaveWorkflowFenced(ctx context.Context, id string, lease Lease, result orchestration.Result) error {
	researchJSON, err := optionalJSON(result.Evidence)
	if err != nil {
		return err
	}
	judgmentJSON, err := optionalJSON(result.Judgment)
	if err != nil {
		return err
	}
	thesisJSON, err := optionalJSON(result.Thesis)
	if err != nil {
		return err
	}
	atomsJSON, err := optionalJSON(result.Atoms)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	stamp, nowNS := leaseTimestamp(now)
	updated, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET research_json=?,judgment_json=?,thesis_json=?,atoms_json=?,updated_at=?,checkpoint=? WHERE id=? AND EXISTS (SELECT 1 FROM pipeline_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?)`, researchJSON, judgmentJSON, thesisJSON, atomsJSON, stamp, "judgment_persisted", id, id, lease.Owner, lease.Token, nowNS)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return fencedWriteError(ctx, tx, id, lease, stamp)
	}
	if err := insertEventTx(ctx, tx, id, "judgment_completed", "judgment_persisted", map[string]any{"atom_count": len(result.Atoms)}); err != nil {
		return err
	}
	return tx.Commit()
}

// SetChildrenFenced persists child distribution relationships atomically.
func (s *Store) SetChildrenFenced(ctx context.Context, id string, lease Lease, children []ChildRun, checkpoint string) error {
	childrenJSON, err := json.Marshal(children)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(children))
	for _, child := range children {
		ids = append(ids, child.ID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now, nowNS := leaseTimestamp(time.Now())
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET children_json=?,updated_at=?,checkpoint=? WHERE id=? AND EXISTS (SELECT 1 FROM pipeline_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?)`, childrenJSON, now, checkpoint, id, id, lease.Owner, lease.Token, nowNS)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fencedWriteError(ctx, tx, id, lease, now)
	}
	if err := insertEventTx(ctx, tx, id, "children_created", checkpoint, map[string]any{"child_run_ids": ids}); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateChildrenFenced records the latest child observations and event.
func (s *Store) UpdateChildrenFenced(ctx context.Context, id string, lease Lease, children []ChildRun, checkpoint string) error {
	childrenJSON, err := json.Marshal(children)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now, nowNS := leaseTimestamp(time.Now())
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET children_json=?,updated_at=?,checkpoint=? WHERE id=? AND EXISTS (SELECT 1 FROM pipeline_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?)`, childrenJSON, now, checkpoint, id, id, lease.Owner, lease.Token, nowNS)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fencedWriteError(ctx, tx, id, lease, now)
	}
	if err := insertEventTx(ctx, tx, id, "children_observed", checkpoint, map[string]any{"children": children}); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateRun persists a state transition and appends its event.
func (s *Store) UpdateRun(ctx context.Context, id string, status PipelineStatus, checkpoint string, block *domain.CapabilityBlock, runErr *domain.RunError, payload map[string]any) error {
	return s.updateRun(ctx, id, nil, status, checkpoint, block, runErr, payload)
}

// UpdateRunFenced persists a transition only for the current lease.
func (s *Store) UpdateRunFenced(ctx context.Context, id string, lease Lease, status PipelineStatus, checkpoint string, block *domain.CapabilityBlock, runErr *domain.RunError, payload map[string]any) error {
	return s.updateRun(ctx, id, &lease, status, checkpoint, block, runErr, payload)
}

func (s *Store) updateRun(ctx context.Context, id string, lease *Lease, status PipelineStatus, checkpoint string, block *domain.CapabilityBlock, runErr *domain.RunError, payload map[string]any) error {
	blockJSON, err := optionalJSON(block)
	if err != nil {
		return err
	}
	errorJSON, err := optionalJSON(runErr)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	stamp, nowNS := leaseTimestamp(now)
	args := []any{status, checkpoint, blockJSON, errorJSON, stamp, nil, nil, id}
	query := `UPDATE pipeline_runs SET status=?,checkpoint=?,capability_block_json=?,error_json=?,updated_at=?,started_at=COALESCE(started_at,?),finished_at=? WHERE id=?`
	if status == PipelineStatusRunning {
		args[5] = stamp
	}
	if terminal(status) {
		args[6] = stamp
	}
	if lease != nil {
		query += ` AND EXISTS (SELECT 1 FROM pipeline_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?)`
		args = append(args, id, lease.Owner, lease.Token, nowNS)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		if lease != nil {
			return fencedWriteError(ctx, tx, id, *lease, stamp)
		}
		return ErrRunNotFound
	}
	if err := insertEventTx(ctx, tx, id, string(status), checkpoint, payload); err != nil {
		return err
	}
	return tx.Commit()
}

// RetryRun atomically requeues a terminal run, advances its attempt, and
// revokes the previous worker lease. The bool reports whether it transitioned.
func (s *Store) RetryRun(ctx context.Context, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	var attempt int
	err = tx.QueryRowContext(ctx, `UPDATE pipeline_runs SET attempt=attempt+1,status=?,checkpoint=?,capability_block_json=NULL,error_json=NULL,finished_at=NULL,updated_at=? WHERE id=? AND status IN (?,?,?) RETURNING attempt`, PipelineStatusQueued, "retry_queued", stamp, id, PipelineStatusBlocked, PipelineStatusFailed, PipelineStatusCancelled).Scan(&attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_leases WHERE run_id=?`, id); err != nil {
		return false, err
	}
	if err := insertEventTx(ctx, tx, id, string(PipelineStatusQueued), "retry_queued", map[string]any{"attempt": attempt}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// AppendEvent appends an event to the pipeline ledger.
func (s *Store) AppendEvent(ctx context.Context, id, eventType, checkpoint string, payload map[string]any) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pipeline_events (run_id,type,checkpoint,payload_json,created_at) VALUES (?,?,?,?,?)`, id, eventType, checkpoint, nullableJSON(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// ListEvents returns append-only events after eventID.
func (s *Store) ListEvents(ctx context.Context, id string, afterID int64, limit int) ([]domain.RunEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,type,checkpoint,payload_json,created_at FROM pipeline_events WHERE run_id=? AND id>? ORDER BY id LIMIT ?`, id, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.RunEvent
	for rows.Next() {
		var e domain.RunEvent
		var payload, stamp sql.NullString
		if err := rows.Scan(&e.ID, &e.RunID, &e.Type, &e.Checkpoint, &payload, &stamp); err != nil {
			return nil, err
		}
		e.Payload = map[string]any{}
		if payload.Valid && payload.String != "" {
			if err := json.Unmarshal([]byte(payload.String), &e.Payload); err != nil {
				return nil, err
			}
		}
		var err error
		e.CreatedAt, err = time.Parse(time.RFC3339Nano, stamp.String)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// AcquireLease claims a run lease and returns a fresh fencing token.
func (s *Store) AcquireLease(ctx context.Context, id, owner string, ttl time.Duration) (Lease, error) {
	if owner == "" || ttl <= 0 {
		return Lease{}, ErrLeaseBusy
	}
	now := time.Now().UTC()
	nowStamp, nowNS := leaseTimestamp(now)
	untilStamp, untilNS := leaseTimestamp(now.Add(ttl))
	lease := Lease{Owner: owner, Token: uuid.NewString()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_leases(run_id,owner,token,lease_until,lease_until_ns,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET owner=excluded.owner,token=excluded.token,lease_until=excluded.lease_until,lease_until_ns=excluded.lease_until_ns,updated_at=excluded.updated_at WHERE pipeline_leases.lease_until_ns<=?`, id, lease.Owner, lease.Token, untilStamp, untilNS, nowStamp, nowNS)
	if err != nil {
		return Lease{}, err
	}
	var current Lease
	if err := tx.QueryRowContext(ctx, `SELECT owner,token FROM pipeline_leases WHERE run_id=?`, id).Scan(&current.Owner, &current.Token); err != nil {
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

// RenewLease extends a lease for its current fencing epoch.
func (s *Store) RenewLease(ctx context.Context, id string, lease Lease, ttl time.Duration) error {
	now := time.Now().UTC()
	stamp, nowNS := leaseTimestamp(now)
	untilStamp, untilNS := leaseTimestamp(now.Add(ttl))
	result, err := s.db.ExecContext(ctx, `UPDATE pipeline_leases SET lease_until=?,lease_until_ns=?,updated_at=? WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?`, untilStamp, untilNS, stamp, id, lease.Owner, lease.Token, nowNS)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrLeaseLost
	}
	return nil
}

// ReleaseLeaseToken releases only the matching fencing epoch.
func (s *Store) ReleaseLeaseToken(ctx context.Context, id string, lease Lease) error {
	_, nowNS := leaseTimestamp(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM pipeline_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?`, id, lease.Owner, lease.Token, nowNS)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 1 {
		return tx.Commit()
	}
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM pipeline_leases WHERE run_id=? AND owner=? AND token=?`, id, lease.Owner, lease.Token).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return err
	}
	return ErrLeaseLost
}

// CancelRun marks a non-terminal pipeline cancelled and revokes its lease.
func (s *Store) CancelRun(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET status=?,checkpoint=?,updated_at=?,finished_at=? WHERE id=? AND status IN (?,?)`, PipelineStatusCancelled, "cancelled", now, now, id, PipelineStatusQueued, PipelineStatusRunning)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM pipeline_runs WHERE id=?`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRunNotFound
			}
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_leases WHERE run_id=?`, id); err != nil {
		return err
	}
	if err := insertEventTx(ctx, tx, id, "cancelled", "cancelled", map[string]any{"reason": "api_cancel"}); err != nil {
		return err
	}
	return tx.Commit()
}

type scanRow interface{ Scan(...any) error }

func (s *Store) scanRun(row scanRow) (PipelineRun, error) {
	var run PipelineRun
	var status, requestJSON string
	var researchJSON, judgmentJSON, thesisJSON, atomsJSON, childrenJSON sql.NullString
	var checkpoint, blockJSON, errorJSON sql.NullString
	var attempt int
	var created, updated string
	var started, finished sql.NullString
	if err := row.Scan(&run.ID, &status, &requestJSON, &researchJSON, &judgmentJSON, &thesisJSON, &atomsJSON, &childrenJSON, &checkpoint, &blockJSON, &errorJSON, &attempt, &created, &updated, &started, &finished); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PipelineRun{}, ErrRunNotFound
		}
		return PipelineRun{}, err
	}
	run.Status, run.Checkpoint, run.Attempt = PipelineStatus(status), checkpoint.String, attempt
	if err := json.Unmarshal([]byte(requestJSON), &run.Request); err != nil {
		return PipelineRun{}, err
	}
	if researchJSON.Valid && researchJSON.String != "" {
		if err := json.Unmarshal([]byte(researchJSON.String), &run.Research); err != nil {
			return PipelineRun{}, err
		}
	}
	if judgmentJSON.Valid && judgmentJSON.String != "" {
		if err := json.Unmarshal([]byte(judgmentJSON.String), &run.Judgment); err != nil {
			return PipelineRun{}, err
		}
	}
	if thesisJSON.Valid && thesisJSON.String != "" {
		if err := json.Unmarshal([]byte(thesisJSON.String), &run.Thesis); err != nil {
			return PipelineRun{}, err
		}
	}
	if atomsJSON.Valid && atomsJSON.String != "" {
		if err := json.Unmarshal([]byte(atomsJSON.String), &run.Atoms); err != nil {
			return PipelineRun{}, err
		}
	}
	if childrenJSON.Valid && childrenJSON.String != "" {
		if err := json.Unmarshal([]byte(childrenJSON.String), &run.Children); err != nil {
			return PipelineRun{}, err
		}
		for _, child := range run.Children {
			run.ChildRunIDs = append(run.ChildRunIDs, child.ID)
		}
	}
	if blockJSON.Valid && blockJSON.String != "" {
		run.CapabilityBlock = &domain.CapabilityBlock{}
		if err := json.Unmarshal([]byte(blockJSON.String), run.CapabilityBlock); err != nil {
			return PipelineRun{}, err
		}
	}
	if errorJSON.Valid && errorJSON.String != "" {
		run.Error = &domain.RunError{}
		if err := json.Unmarshal([]byte(errorJSON.String), run.Error); err != nil {
			return PipelineRun{}, err
		}
	}
	var err error
	run.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return PipelineRun{}, err
	}
	run.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return PipelineRun{}, err
	}
	if started.Valid {
		value, e := time.Parse(time.RFC3339Nano, started.String)
		if e != nil {
			return PipelineRun{}, e
		}
		run.StartedAt = &value
	}
	if finished.Valid {
		value, e := time.Parse(time.RFC3339Nano, finished.String)
		if e != nil {
			return PipelineRun{}, e
		}
		run.FinishedAt = &value
	}
	return run, nil
}

func insertEventTx(ctx context.Context, tx *sql.Tx, runID, eventType, checkpoint string, payload map[string]any) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO pipeline_events(run_id,type,checkpoint,payload_json,created_at) VALUES(?,?,?,?,?)`, runID, eventType, checkpoint, nullableJSON(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func fencedWriteError(ctx context.Context, tx *sql.Tx, id string, lease Lease, now string) error {
	var one int
	parsed, err := parseLeaseTimestamp(now)
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM pipeline_leases WHERE run_id=? AND owner=? AND token=? AND lease_until_ns>?`, id, lease.Owner, lease.Token, parsed).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	return ErrRunNotFound
}

func optionalJSON(value any) (any, error) {
	if value == nil {
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
