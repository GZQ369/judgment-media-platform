package egopublisher

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/publication"
	_ "modernc.org/sqlite"
)

// referenceStore persists the opaque Ego task-space reference independently
// from the publication lease. The reservation is hashed, never returned by an
// HTTP response, and is required to load the reference after a restart.
type referenceStore struct {
	db *sql.DB
}

func openReferenceStore(root string, platform publication.Platform) (*referenceStore, error) {
	if strings.TrimSpace(root) == "" || platform == "" {
		return nil, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create reference store directory: %w", err)
	}
	path := filepath.Join(filepath.Clean(root), "ego-"+string(platform)+"-references.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open reference store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure reference store: %w", err)
		}
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS ego_task_references(
 publication_id TEXT PRIMARY KEY,
 reservation_hash TEXT NOT NULL,
 package_fingerprint TEXT NOT NULL,
 task_space_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate reference store: %w", err)
	}
	return &referenceStore{db: db}, nil
}

func (s *referenceStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *referenceStore) put(publicationID, reservation, fingerprint, taskSpaceID string) error {
	if s == nil || s.db == nil {
		return errors.New("reference store is unavailable")
	}
	reservationHash := digestSecret(reservation)
	if reservationHash == "" || strings.TrimSpace(publicationID) == "" || strings.TrimSpace(fingerprint) == "" || strings.TrimSpace(taskSpaceID) == "" {
		return errors.New("reference identity is incomplete")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT INTO ego_task_references(publication_id,reservation_hash,package_fingerprint,task_space_id,created_at,updated_at)
VALUES(?,?,?,?,?,?) ON CONFLICT(publication_id) DO UPDATE SET
 reservation_hash=excluded.reservation_hash, package_fingerprint=excluded.package_fingerprint,
 task_space_id=excluded.task_space_id, updated_at=excluded.updated_at
WHERE ego_task_references.reservation_hash=excluded.reservation_hash
  AND ego_task_references.package_fingerprint=excluded.package_fingerprint
  AND ego_task_references.task_space_id=excluded.task_space_id`, publicationID, reservationHash, fingerprint, taskSpaceID, now, now)
	if err != nil {
		return err
	}
	var savedReservation, savedFingerprint, savedTask string
	err = s.db.QueryRow(`SELECT reservation_hash,package_fingerprint,task_space_id FROM ego_task_references WHERE publication_id=?`, publicationID).Scan(&savedReservation, &savedFingerprint, &savedTask)
	if err != nil {
		return err
	}
	if savedReservation != reservationHash || savedFingerprint != fingerprint || savedTask != taskSpaceID {
		return errors.New("reference identity is already bound to a different task space")
	}
	return nil
}

func (s *referenceStore) get(publicationID, reservation, fingerprint string) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("reference store is unavailable")
	}
	if digestSecret(reservation) == "" || strings.TrimSpace(publicationID) == "" || strings.TrimSpace(fingerprint) == "" {
		return "", errors.New("reference identity is incomplete")
	}
	var reservationHash, savedFingerprint, taskSpaceID string
	err := s.db.QueryRow(`SELECT reservation_hash,package_fingerprint,task_space_id FROM ego_task_references WHERE publication_id=?`, publicationID).Scan(&reservationHash, &savedFingerprint, &taskSpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("task-space reference not found")
	}
	if err != nil {
		return "", err
	}
	if reservationHash != digestSecret(reservation) || savedFingerprint != fingerprint {
		return "", errors.New("task-space reference binding mismatch")
	}
	return taskSpaceID, nil
}

func digestSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func packageBinding(pkg publication.Package) (string, error) {
	raw, err := json.Marshal(pkg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
