package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type errRow struct{ err error }

func (e errRow) Scan(...any) error { return e.err }

func (db *DB) closed() error {
	if db == nil || db.Pool == nil {
		return errors.New("database is not open")
	}
	return nil
}

// Exec implements the sqlc DBTX port.
func (db *DB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := db.closed(); err != nil {
		return pgconn.CommandTag{}, err
	}
	return db.Pool.Exec(ctx, sql, args...)
}

// Query implements the sqlc DBTX port.
func (db *DB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := db.closed(); err != nil {
		return nil, err
	}
	return db.Pool.Query(ctx, sql, args...)
}

// QueryRow implements the sqlc DBTX port.
func (db *DB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := db.closed(); err != nil {
		return errRow{err: err}
	}
	return db.Pool.QueryRow(ctx, sql, args...)
}

// Begin starts a transaction.
func (db *DB) Begin(ctx context.Context) (pgx.Tx, error) {
	if err := db.closed(); err != nil {
		return nil, err
	}
	return db.Pool.Begin(ctx)
}
