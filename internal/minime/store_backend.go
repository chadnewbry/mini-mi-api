package minime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

var errStoreNotFound = errors.New("store snapshot not found")
var errStoreVersionConflict = errors.New("store version conflict")

type storeBackend interface {
	Load(ctx context.Context) ([]byte, int64, error)
	Save(ctx context.Context, data []byte, expectedVersion int64) (int64, error)
}

type fileStoreBackend struct {
	path string
}

func newFileStoreBackend(path string) storeBackend {
	return fileStoreBackend{path: path}
}

func (b fileStoreBackend) Load(_ context.Context) ([]byte, int64, error) {
	if _, err := os.Stat(b.path); errors.Is(err, os.ErrNotExist) {
		return nil, 0, errStoreNotFound
	}

	data, err := os.ReadFile(b.path)
	if err != nil {
		return nil, 0, fmt.Errorf("read store file: %w", err)
	}
	return data, 1, nil
}

func (b fileStoreBackend) Save(_ context.Context, data []byte, _ int64) (int64, error) {
	tempFile, err := os.CreateTemp(filepath.Dir(b.path), filepath.Base(b.path)+".*.tmp")
	if err != nil {
		return 0, fmt.Errorf("create store temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return 0, fmt.Errorf("write store temp file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return 0, fmt.Errorf("close store temp file: %w", err)
	}
	if err := os.Rename(tempPath, b.path); err != nil {
		return 0, fmt.Errorf("replace store file: %w", err)
	}
	return 1, nil
}

type postgresStoreBackend struct {
	db        *sql.DB
	tableName string
}

func newPostgresStoreBackend(ctx context.Context, databaseURL, tableName string) (storeBackend, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database url is required for postgres store backend")
	}

	safeTableName, err := sanitizeSQLIdentifier(tableName)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}

	pingContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingContext); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	backend := &postgresStoreBackend{
		db:        db,
		tableName: safeTableName,
	}
	if err := backend.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return backend, nil
}

func (b *postgresStoreBackend) ensureSchema(ctx context.Context) error {
	query := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  key TEXT PRIMARY KEY,
  version BIGINT NOT NULL,
  payload JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`, b.tableName)
	if _, err := b.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("create postgres store table: %w", err)
	}
	return nil
}

func (b *postgresStoreBackend) Load(ctx context.Context) ([]byte, int64, error) {
	query := fmt.Sprintf(`SELECT payload::text, version FROM %s WHERE key = $1`, b.tableName)
	var data string
	var version int64
	if err := b.db.QueryRowContext(ctx, query, "primary").Scan(&data, &version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, errStoreNotFound
		}
		return nil, 0, fmt.Errorf("load postgres store snapshot: %w", err)
	}
	return []byte(data), version, nil
}

func (b *postgresStoreBackend) Save(ctx context.Context, data []byte, expectedVersion int64) (int64, error) {
	if expectedVersion <= 0 {
		query := fmt.Sprintf(
			`INSERT INTO %s (key, version, payload, updated_at) VALUES ($1, 1, $2::jsonb, NOW()) ON CONFLICT (key) DO NOTHING`,
			b.tableName,
		)
		result, err := b.db.ExecContext(ctx, query, "primary", string(data))
		if err != nil {
			return 0, fmt.Errorf("insert postgres store snapshot: %w", err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("read postgres insert rowcount: %w", err)
		}
		if rowsAffected == 0 {
			return 0, errStoreVersionConflict
		}
		return 1, nil
	}

	query := fmt.Sprintf(
		`UPDATE %s SET version = version + 1, payload = $1::jsonb, updated_at = NOW() WHERE key = $2 AND version = $3 RETURNING version`,
		b.tableName,
	)
	var newVersion int64
	if err := b.db.QueryRowContext(ctx, query, string(data), "primary", expectedVersion).Scan(&newVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errStoreVersionConflict
		}
		return 0, fmt.Errorf("update postgres store snapshot: %w", err)
	}
	return newVersion, nil
}

var sqlIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func sanitizeSQLIdentifier(value string) (string, error) {
	candidate := strings.TrimSpace(value)
	if candidate == "" {
		candidate = "minime_store_snapshots"
	}
	if !sqlIdentifierPattern.MatchString(candidate) {
		return "", fmt.Errorf("invalid sql identifier %q", value)
	}
	return candidate, nil
}
