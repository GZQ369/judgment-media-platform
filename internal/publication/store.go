package publication

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const schema = `
CREATE TABLE IF NOT EXISTS pub_schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pub_settings(id INTEGER PRIMARY KEY CHECK(id=1), location TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pub_runs(
 creator TEXT NOT NULL, id TEXT NOT NULL, business_day TEXT NOT NULL, atom_revision TEXT NOT NULL,
 PRIMARY KEY(creator,id), UNIQUE(creator,business_day));
CREATE TABLE IF NOT EXISTS pub_policies(version TEXT PRIMARY KEY, policy_json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pub_authorizations(
 id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, policy_version TEXT NOT NULL,
 creator TEXT NOT NULL, account TEXT NOT NULL, platform TEXT NOT NULL, run_id TEXT NOT NULL,
 variant_id TEXT NOT NULL, expires_at TEXT NOT NULL, consumed_at TEXT);
CREATE TABLE IF NOT EXISTS pub_publications(
 id TEXT PRIMARY KEY, creator TEXT NOT NULL, run_id TEXT NOT NULL, account TEXT NOT NULL,
 platform TEXT NOT NULL, variant_id TEXT NOT NULL, package_json TEXT NOT NULL,
 fingerprint TEXT NOT NULL, policy_version TEXT NOT NULL, business_day TEXT NOT NULL,
 authorization_id TEXT NOT NULL UNIQUE, status TEXT NOT NULL CHECK(status IN
 ('draft_ready','committing','submitted','reviewing','published','rejected','unknown','failed')),
 reservation_token TEXT NOT NULL DEFAULT '', operation_token TEXT NOT NULL DEFAULT '',
 lease_until INTEGER NOT NULL DEFAULT 0, external_id TEXT NOT NULL DEFAULT '',
 external_url TEXT NOT NULL DEFAULT '', evidence TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
 reserved_at TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(creator,run_id,platform,account,variant_id));
`

func (o *Orchestrator) Migrate(ctx context.Context) error {
	return o.withTx(ctx, func(tx *sql.Tx) error {
		// The unreleased v1 draft lacks creator and digest identity. Never invent
		// identity for previously authorized or potentially submitted content.
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='pub_runs'`).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			var columns int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('pub_runs') WHERE name='creator'`).Scan(&columns); err != nil {
				return err
			}
			if columns == 0 {
				return errors.New("publication schema blocker: legacy draft schema lacks creator/digest identity; audit old reservations before using a fresh publication database")
			}
		}
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return err
		}
		var newer int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pub_schema_migrations WHERE version>2`).Scan(&newer); err != nil {
			return err
		}
		if newer > 0 {
			return errors.New("publication schema is newer than supported version 2")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO pub_schema_migrations VALUES(2,?)`, formatTime(o.now())); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO pub_settings VALUES(1,?)`, o.location.String()); err != nil {
			return err
		}
		var location string
		if err := tx.QueryRowContext(ctx, `SELECT location FROM pub_settings WHERE id=1`).Scan(&location); err != nil {
			return err
		}
		if location != o.location.String() {
			return errors.New("publication quota timezone differs from database configuration")
		}
		return nil
	})
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const publicationSelect = `SELECT id,package_json,fingerprint,policy_version,business_day,authorization_id,status,
 reservation_token,operation_token,lease_until,external_id,external_url,evidence,last_error,reserved_at,created_at,updated_at FROM pub_publications`

func loadPublication(ctx context.Context, q queryer, id string) (Publication, error) {
	var p Publication
	var raw, reserved, created, updated string
	err := q.QueryRowContext(ctx, publicationSelect+` WHERE id=?`, id).Scan(&p.ID, &raw, &p.Fingerprint, &p.PolicyVersion, &p.BusinessDay, &p.AuthorizationID, &p.Status, &p.ReservationToken, &p.operationToken, &p.leaseUntil, &p.ExternalID, &p.ExternalURL, &p.Evidence, &p.LastError, &reserved, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(raw), &p.Package); err != nil {
		return p, err
	}
	for _, item := range []struct {
		s string
		t *time.Time
	}{{reserved, &p.ReservedAt}, {created, &p.CreatedAt}, {updated, &p.UpdatedAt}} {
		if item.s != "" {
			*item.t, err = time.Parse(time.RFC3339Nano, item.s)
			if err != nil {
				return p, err
			}
		}
	}
	fp, err := p.Package.Fingerprint()
	if err != nil {
		return p, err
	}
	if fp != p.Fingerprint {
		return p, ErrAuthorizationMismatch
	}
	return p, nil
}
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (o *Orchestrator) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	for attempt := 0; attempt < 20; attempt++ {
		tx, err := o.db.BeginTx(ctx, nil)
		if err == nil {
			// Acquire the SQLite write lock before reads to avoid deferred upgrade races.
			_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS pub_lock(id INTEGER PRIMARY KEY)`)
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO pub_lock VALUES(1)`)
			}
			if err == nil {
				err = fn(tx)
			}
			if err == nil {
				err = tx.Commit()
			}
			_ = tx.Rollback()
		}
		if err == nil {
			return nil
		}
		var code interface{ Code() int }
		if !errors.As(err, &code) || (code.Code()&255 != 5 && code.Code()&255 != 6) {
			return err
		}
		if attempt == 19 {
			return fmt.Errorf("publication SQLite busy: %w", err)
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("publication transaction exhausted")
}
