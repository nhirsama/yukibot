package database

import (
	"context"

	"github.com/nhirsama/yukibot/internal/contracts"
)

// Feature opens PostgreSQL, applies migrations, and closes the pool.
type Feature struct {
	db     *DB
	runner *Runner
}

// NewFeature returns the database lifecycle component. The pool opens in Start.
func NewFeature(databaseURL string, migrations []contracts.Migration) (*Feature, error) {
	db := New(databaseURL)
	runner, err := NewRunner(db, migrations)
	if err != nil {
		return nil, err
	}
	return &Feature{db: db, runner: runner}, nil
}

// DB is the shared pool handle. It is usable after Start succeeds.
func (f *Feature) DB() *DB {
	if f == nil {
		return nil
	}
	return f.db
}

// Name is the lifecycle name.
func (f *Feature) Name() string { return "database" }

// Start connects and applies migrations. A failed migration closes the pool.
func (f *Feature) Start(ctx context.Context) error {
	if err := f.db.Open(ctx); err != nil {
		return err
	}
	if _, err := f.runner.Upgrade(ctx); err != nil {
		f.db.Close()
		return err
	}
	return nil
}

// Stop closes the pool. It is safe when the pool was never opened.
func (f *Feature) Stop(context.Context) error {
	if f != nil && f.db != nil {
		f.db.Close()
	}
	return nil
}
