// Package database owns the process-wide SQLite connection and atomic schema
// creation shared by product-domain persistence adapters.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens and configures the single SQLite connection shared by adapters.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := Configure(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Configure applies settings required by every SQLite adapter.
func Configure(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("database is required")
	}
	for _, setting := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, setting); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

// CreateSchema creates a product adapter's tables and indexes in one transaction.
// Schemas must use IF NOT EXISTS so reopening a store preserves existing data.
// Incompatible databases must be deleted and recreated when the schema changes.
func CreateSchema(ctx context.Context, db *sql.DB, schema string) error {
	if db == nil {
		return errors.New("database is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	return tx.Commit()
}
