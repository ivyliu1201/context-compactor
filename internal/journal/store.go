// Package journal persists privacy-filtered event metadata, mutation
// operations, and resume checkpoints in a repository-local SQLite database.
package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	defaultRelativePath = ".context-compactor/context.db"
	defaultBusyTimeout  = 5000
)

// ErrConflict indicates that a stable identifier was reused with different
// durable data or that a monotonic cursor would move backward.
var ErrConflict = errors.New("journal identifier conflict")

type OpenOptions struct {
	ProjectRoot string
	Path        string
}

type Store struct {
	db          *sql.DB
	projectRoot string
	path        string
}

func Open(ctx context.Context, options OpenOptions) (*Store, error) {
	projectRoot, err := CanonicalProjectRoot(options.ProjectRoot)
	if err != nil {
		return nil, err
	}
	databasePath, err := ResolveDatabasePath(projectRoot, options.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}

	db, err := sql.Open("sqlite", sqliteDSN(databasePath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite journal: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db, projectRoot: projectRoot, path: databasePath}
	if err := store.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("restrict journal file permissions: %w", err)
	}
	return store, nil
}

func sqliteDSN(databasePath string) string {
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", defaultBusyTimeout))
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Set("_txlock", "immediate")
	urlPath := filepath.ToSlash(databasePath)
	if len(urlPath) >= 2 && urlPath[1] == ':' {
		urlPath = "/" + urlPath
	}
	location := url.URL{
		Scheme:   "file",
		Path:     urlPath,
		RawQuery: query.Encode(),
	}
	return location.String()
}

func (store *Store) initialize(ctx context.Context) error {
	if err := store.db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect sqlite journal: %w", err)
	}

	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable WAL journal mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("enable WAL journal mode: SQLite returned %q", journalMode)
	}

	if err := verifyPragmas(ctx, store.db); err != nil {
		return err
	}
	if err := applyMigrations(ctx, store.db); err != nil {
		return err
	}
	return nil
}

func verifyPragmas(ctx context.Context, db *sql.DB) error {
	checks := []struct {
		query string
		want  int
		name  string
	}{
		{query: "PRAGMA synchronous", want: 2, name: "synchronous=FULL"},
		{query: "PRAGMA foreign_keys", want: 1, name: "foreign_keys=ON"},
		{query: "PRAGMA busy_timeout", want: defaultBusyTimeout, name: "busy_timeout=5000"},
	}
	for _, check := range checks {
		var got int
		if err := db.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			return fmt.Errorf("verify %s: %w", check.name, err)
		}
		if got != check.want {
			return fmt.Errorf("verify %s: got %d", check.name, got)
		}
	}
	return nil
}

func (store *Store) Close() error {
	return store.db.Close()
}

func (store *Store) Path() string {
	return store.path
}

func (store *Store) ProjectRoot() string {
	return store.projectRoot
}
