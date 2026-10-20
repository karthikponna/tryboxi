// Package store owns the SQLite database: one writer connection, a reader
// pool, and embedded goose migrations.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/karthikponna/tryboxi/internal/store/db"
	"github.com/karthikponna/tryboxi/migrations"
)

// Store bundles the writer and reader query sets.
type Store struct {
	W    *db.Queries // single writer connection
	R    *db.Queries // reader pool
	wdb  *sql.DB
	rdb  *sql.DB
	path string
}

func dsn(path string, ro bool) string {
	s := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	if ro {
		s += "&mode=ro"
	}
	return s
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	wdb, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	wdb.SetMaxOpenConns(1)
	if err := wdb.PingContext(ctx); err != nil {
		wdb.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		wdb.Close()
		return nil, err
	}
	if err := goose.UpContext(ctx, wdb, "."); err != nil {
		wdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	rdb, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		wdb.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(8)
	return &Store{W: db.New(wdb), R: db.New(rdb), wdb: wdb, rdb: rdb, path: path}, nil
}

// Tx runs fn inside a write transaction.
func (s *Store) Tx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(db.New(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Close closes both pools.
func (s *Store) Close() error {
	_ = s.rdb.Close()
	return s.wdb.Close()
}

// NullString helpers keep call sites short.
func NS(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// NI64 wraps an int64 as a valid NullInt64.
func NI64(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

// Bool converts to SQLite's integer boolean.
func Bool(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
