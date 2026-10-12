package runstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

// RetryOperationKey encodes field boundaries and a version before hashing.
func RetryOperationKey(parentID string, parentAttempt int, childID string) string {
	data, _ := json.Marshal(struct {
		Version       int    `json:"version"`
		ParentID      string `json:"parent_id"`
		ParentAttempt int    `json:"parent_attempt"`
		ChildID       string `json:"child_id"`
	}{1, parentID, parentAttempt, childID})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// RetryOnce consumes a parent/attempt/child operation even when the child is
// active or ready. Receipt replay only reads current state, never requeues it.
func (s *Store) RetryOnce(ctx context.Context, parentID string, parentAttempt int, childID string) (domain.Run, error) {
	if strings.TrimSpace(parentID) == "" || parentAttempt <= 0 || strings.TrimSpace(childID) == "" {
		return domain.Run{}, fmt.Errorf("retry once requires parent ID, positive parent attempt, and child ID")
	}
	key := RetryOperationKey(parentID, parentAttempt, childID)
	for attempt := 0; ; attempt++ {
		err := s.retryOnceTransaction(ctx, key, parentID, parentAttempt, childID)
		if err == nil {
			return s.GetRun(ctx, childID)
		}
		var sqliteErr interface{ Code() int }
		if !errors.As(err, &sqliteErr) || (sqliteErr.Code()&255 != 5 && sqliteErr.Code()&255 != 6) || attempt >= 3 {
			return domain.Run{}, err
		}
		timer := time.NewTimer(time.Duration(10<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.Run{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// Each retry owns a fresh transaction; a busy error always rolls back all work.
func (s *Store) retryOnceTransaction(ctx context.Context, key, parentID string, parentAttempt int, childID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	// Acquire SQLite's write lock before reading any state, including on replay.
	inserted, err := tx.ExecContext(ctx, `INSERT INTO distribution_retry_receipts(operation_key,parent_id,parent_attempt,child_id,child_attempt,outcome,created_at) SELECT ?,?,?,id,attempt,'noop',? FROM runs WHERE id=? ON CONFLICT DO NOTHING`, key, parentID, parentAttempt, stamp, childID)
	if err != nil {
		return err
	}
	n, err := inserted.RowsAffected()
	if err != nil {
		return err
	}
	var recordedParent, recordedChild string
	var recordedAttempt int
	err = tx.QueryRowContext(ctx, `SELECT parent_id,parent_attempt,child_id FROM distribution_retry_receipts WHERE operation_key=?`, key).Scan(&recordedParent, &recordedAttempt, &recordedChild)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE id=?`, childID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrRunNotFound
		}
		return fmt.Errorf("retry receipt missing for operation: %w", ErrIdempotencyConflict)
	}
	if err != nil {
		return err
	}
	if recordedParent != parentID || recordedAttempt != parentAttempt || recordedChild != childID {
		return fmt.Errorf("retry receipt identity mismatch: %w", ErrIdempotencyConflict)
	}
	if n == 1 {
		updated, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,attempt=attempt+1,checkpoint='retry_queued',capability_block_json=NULL,error_json=NULL,finished_at=NULL,updated_at=? WHERE id=? AND status IN (?,?,?)`, domain.RunStatusQueued, stamp, childID, domain.RunStatusBlocked, domain.RunStatusFailed, domain.RunStatusCancelled)
		if err != nil {
			return err
		}
		changed, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		outcome := "noop"
		if changed == 1 {
			outcome = "queued"
		}
		var attempt int
		if err := tx.QueryRowContext(ctx, `SELECT attempt FROM runs WHERE id=?`, childID).Scan(&attempt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE distribution_retry_receipts SET child_attempt=?,outcome=? WHERE operation_key=?`, attempt, outcome, key); err != nil {
			return err
		}
		if err := insertEventTx(ctx, tx, childID, "retry_once", "retry_"+outcome, map[string]any{"operation_key": key, "parent_id": parentID, "parent_attempt": parentAttempt, "child_attempt": attempt, "outcome": outcome}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
