package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// DB is the process-wide PostgreSQL pool.
// Open may run later; queries fail until the pool exists.
type DB struct {
	url  string
	Pool *pgxpool.Pool
}

// New returns an unopened database. Lifecycle start opens it.
func New(databaseURL string) *DB {
	return &DB{url: databaseURL}
}

// Open connects to a postgres URL.
func Open(ctx context.Context, databaseURL string) (*DB, error) {
	db := New(databaseURL)
	if err := db.Open(ctx); err != nil {
		return nil, err
	}
	return db, nil
}

// Open connects and pings. A second call does nothing.
func (db *DB) Open(ctx context.Context) error {
	if db == nil {
		return errors.New("database is not configured")
	}
	if db.Pool != nil {
		return nil
	}
	pool, err := pgxpool.New(ctx, db.url)
	if err != nil {
		return err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return err
	}
	db.Pool = pool
	return nil
}

// Close releases the pool. A second call is a no-op.
func (db *DB) Close() {
	if db != nil && db.Pool != nil {
		db.Pool.Close()
		db.Pool = nil
	}
}

// Ping reports whether the database answers.
func (db *DB) Ping(ctx context.Context) bool {
	if db == nil || db.Pool == nil {
		return false
	}
	return db.Pool.Ping(ctx) == nil
}

// Runner applies feature-owned migrations in (scope, version) order.
type Runner struct {
	db         *DB
	migrations []contracts.Migration
}

// NewRunner sorts migrations and rejects duplicate scope/version pairs.
func NewRunner(db *DB, migrations []contracts.Migration) (*Runner, error) {
	copied := append([]contracts.Migration(nil), migrations...)
	for i := 1; i < len(copied); i++ {
		item := copied[i]
		j := i
		for j > 0 && (copied[j-1].Scope > item.Scope || (copied[j-1].Scope == item.Scope && copied[j-1].Version > item.Version)) {
			copied[j] = copied[j-1]
			j--
		}
		copied[j] = item
	}
	seen := map[string]struct{}{}
	for _, migration := range copied {
		if err := migration.Validate(); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s:%d", migration.Scope, migration.Version)
		if _, ok := seen[key]; ok {
			return nil, errors.New("duplicate migration scope and version")
		}
		seen[key] = struct{}{}
	}
	return &Runner{db: db, migrations: copied}, nil
}

// Upgrade applies missing migrations and returns the newly applied pairs.
func (r *Runner) Upgrade(ctx context.Context) ([][2]any, error) {
	if err := r.ensureTable(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.Pool.Query(ctx, `SELECT scope, version, checksum FROM yukibot_schema_migrations`)
	if err != nil {
		return nil, err
	}
	applied := map[string]string{}
	for rows.Next() {
		var scope, checksum string
		var version int
		if err := rows.Scan(&scope, &version, &checksum); err != nil {
			rows.Close()
			return nil, err
		}
		applied[fmt.Sprintf("%s:%d", scope, version)] = checksum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var created [][2]any
	for _, migration := range r.migrations {
		key := fmt.Sprintf("%s:%d", migration.Scope, migration.Version)
		checksum := Checksum(migration.Statements)
		if existing, ok := applied[key]; ok {
			if existing != checksum {
				return created, &contracts.MigrationDriftError{Scope: migration.Scope, Version: migration.Version}
			}
			continue
		}
		if err := r.apply(ctx, migration, checksum); err != nil {
			return created, err
		}
		created = append(created, [2]any{migration.Scope, migration.Version})
	}
	return created, nil
}

func (r *Runner) ensureTable(ctx context.Context) error {
	_, err := r.db.Pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS yukibot_schema_migrations (
    scope text NOT NULL,
    version integer NOT NULL,
    description text NOT NULL,
    checksum text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, version)
)`)
	return err
}

func (r *Runner) apply(ctx context.Context, migration contracts.Migration, checksum string) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, statement := range migration.Statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return mapError(err)
		}
	}
	_, err = tx.Exec(ctx, `
INSERT INTO yukibot_schema_migrations (scope, version, description, checksum)
VALUES ($1, $2, $3, $4)`, migration.Scope, migration.Version, migration.Description, checksum)
	if err != nil {
		return mapError(err)
	}
	return tx.Commit(ctx)
}

// Checksum matches the Python runner: SHA-256 of statements joined by NUL.
func Checksum(statements []string) string {
	sum := sha256.Sum256([]byte(strings.Join(statements, "\x00")))
	return hex.EncodeToString(sum[:])
}

func mapError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code[:2] == "23" {
		return &contracts.IntegrityViolation{Err: err}
	}
	return &contracts.DatabaseError{Err: err}
}

// WithinTransaction runs fn inside one transaction.
func WithinTransaction(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
